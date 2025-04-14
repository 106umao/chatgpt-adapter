package kai

import (
	"bufio"
	"bytes"
	"chatgpt-adapter/core/common"
	"chatgpt-adapter/core/common/vars"
	"chatgpt-adapter/core/gin/inter"
	"chatgpt-adapter/core/gin/model"
	"chatgpt-adapter/core/gin/response"
	"chatgpt-adapter/core/logger"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/iocgo/sdk/env"
)

const (
	ginTokens = "__tokens__"
)

var (
	Model = "kai"
)

type api struct {
	inter.BaseAdapter
	env *env.Environment
}

type kaiRequest struct {
	Question       string   `json:"question"`
	ConversationId int      `json:"conversationId"`
	FileIds        []string `json:"fileIds"`
	ModelType      string   `json:"modelType"`
	WebSearch      int      `json:"webSearch"`
	Citation       string   `json:"citation"`
	AgentId        int      `json:"agentId"`
}

type kaiResponse struct {
	Type           string `json:"type"`
	Reply          string `json:"reply"`
	ChatId         int    `json:"chatId"`
	ReplyChatId    int    `json:"replyChatId"`
	ConversationId int    `json:"conversationId"`
	ContentType    string `json:"contentType"`
	SseType        string `json:"sseType"`
	TraceId        string `json:"traceId"`
}

type kaiDelta struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
}

func (api *api) Name() string {
	return Model
}

func (api *api) Match(ctx *gin.Context, model string) (ok bool, err error) {
	if len(model) <= 4 || Model+"/" != model[:4] {
		return
	}

	switch model[4:] {
	case "CLAUDE_3", "GPT4O", "DEEPSEEK_V3", "DEEPSEEK_R1":
		ok = true
	}
	return
}

func (api *api) Models() (slice []model.Model) {
	models := []string{"CLAUDE_3", "GPT4O", "DEEPSEEK_V3", "DEEPSEEK_R1"}
	for _, m := range models {
		slice = append(slice, model.Model{
			Id:      Model + "/" + m,
			Object:  "model",
			Created: 1686935002,
			By:      Model + "-adapter",
		})
	}
	return
}

func (api *api) ToolChoice(ctx *gin.Context) (ok bool, err error) {
	return false, nil
}

func (api *api) Completion(ctx *gin.Context) (err error) {
	completion := common.GetGinCompletion(ctx)
	model := completion.Model

	// 获取最后一条消息内容
	lastMessage := completion.Messages[len(completion.Messages)-1]
	content, ok := lastMessage["content"].(string)
	if !ok {
		logger.Error("invalid message content")
		return
	}

	// 构建请求
	kaiReq := &kaiRequest{
		Question:       content,
		ConversationId: 278414, // 这里可以根据需要修改
		FileIds:        []string{},
		ModelType:      model, // 这里可以根据model参数映射
		WebSearch:      0,
		Citation:       "",
		AgentId:        -1,
	}

	jsonData, err := json.Marshal(kaiReq)
	if err != nil {
		logger.Error(err)
		return
	}

	req, err := http.NewRequest("POST", "https://kwaipilot.corp.kuaishou.com/api/kwaipilot/conversation/v2/chat", bytes.NewBuffer(jsonData))
	if err != nil {
		logger.Error(err)
		return
	}

	// 设置请求头
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Connection", "keep-alive")
	// 这里可以从环境变量或配置中获取 cookie
	req.Header.Set("Cookie", "your_cookie_here")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		logger.Error(err)
		return
	}

	content = waitResponse(ctx, resp, completion.Stream)
	if content == "" && response.NotResponse(ctx) {
		response.Error(ctx, -1, "EMPTY RESPONSE")
	}
	return
}

func waitResponse(ctx *gin.Context, r *http.Response, sse bool) (content string) {
	created := time.Now().Unix()
	logger.Infof("waitResponse ...")
	tokens := ctx.GetInt(ginTokens)
	onceExec := sync.OnceFunc(func() {
		if !sse {
			ctx.Writer.WriteHeader(http.StatusOK)
		}
	})

	var (
		matchers = common.GetGinMatchers(ctx)
	)

	defer r.Body.Close()
	reader := bufio.NewReader(r.Body)
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			raw := response.ExecMatchers(matchers, "", true)
			if raw != "" && sse {
				response.SSEResponse(ctx, Model, raw, created)
			}
			content += raw
			break
		}

		if err != nil {
			logger.Error(err)
			if response.NotSSEHeader(ctx) {
				response.Error(ctx, -1, err)
			}
			return
		}

		if len(line) > 6 && line[:6] == "data: " {
			data := line[6:]
			if data == "[DONE]" {
				break
			}

			var kaiResp kaiResponse
			if err := json.Unmarshal([]byte(data), &kaiResp); err != nil {
				logger.Warn(err)
				continue
			}

			// 只处理 answer 类型的响应
			if kaiResp.SseType != "answer" {
				continue
			}

			var delta kaiDelta
			if err := json.Unmarshal([]byte(kaiResp.Reply), &delta); err != nil {
				logger.Warn(err)
				continue
			}

			if len(delta.Choices) == 0 {
				continue
			}

			raw := delta.Choices[0].Delta.Content
			if raw == "" {
				continue
			}

			logger.Debug("----- raw -----")
			logger.Debug(raw)
			onceExec()

			raw = response.ExecMatchers(matchers, raw, false)
			if len(raw) == 0 {
				continue
			}

			if raw == response.EOF {
				break
			}

			if sse {
				response.SSEResponse(ctx, Model, raw, created)
			}
			content += raw
		}
	}

	if content == "" && response.NotSSEHeader(ctx) {
		return
	}

	ctx.Set(vars.GinCompletionUsage, response.CalcUsageTokens(content, tokens))
	if !sse {
		response.Response(ctx, Model, content)
	} else {
		response.SSEResponse(ctx, Model, "[DONE]", created)
	}
	return
}
