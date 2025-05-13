package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fastly/compute-sdk-go/cache/simple"
	"github.com/fastly/compute-sdk-go/fsthttp"
	"github.com/fastly/compute-sdk-go/x/exp/handoff"
	"github.com/fastly/go-fastly/v10/fastly"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type GripResponse struct {
	contentType string
	gripHold    string
	gripChannel string
}

type HttpStreamData struct {
	Content string `json:"content"`
	//ContentBin string `json:"content-bin"`
}
type MessageData struct {
	HttpStream HttpStreamData `json:"http-stream"`
}
type PublishItem struct {
	Channel string      `json:"channel"`
	Formats MessageData `json:"formats"`
}
type PublishData struct {
	Items []PublishItem `json:"items"`
}

func NewMCPServer(hooks *server.Hooks) *server.MCPServer {
	mcpServer := server.NewMCPServer("fastly-compute-mcp-server", "0.0.1", server.WithLogging(), server.WithHooks(hooks))
	mcpServer.AddTool(mcp.NewTool("GetLatestGeneratedFastlyVcl",
		mcp.WithDescription("Get the latest generated Fastly VCL associated with the service ID"),
		mcp.WithString("ServiceID",
			mcp.Description("Specify which Service ID to use to get generated VCL"),
			mcp.Required(),
		),
	), handleFastlyGetLatestGeneratedVclTool)
	return mcpServer
}

func handleFastlyGetLatestGeneratedVclTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.Params.Arguments
	serviceId, err := arguments["ServiceID"].(string)
	if !err {
		return nil, fmt.Errorf("invalid ServiceID arguments: %s", err)
	}
	client, _ := fastly.NewClient("__PUT_YOUR_FASTLY_API_TOKEN__")
	client.HTTPClient.Transport = fsthttp.NewTransport("fastly")
	service, _ := client.GetService(&fastly.GetServiceInput{
		ServiceID: serviceId,
	})
	generatedVcl, _ := client.GetGeneratedVCL(&fastly.GetGeneratedVCLInput{
		ServiceID:      serviceId,
		ServiceVersion: int(*service.Environments[0].ServiceVersion),
	})

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: fmt.Sprintf("The latest generated vcl of service ID: %s is as follows;\n%s", serviceId, *generatedVcl.Content),
			},
		},
	}, nil
}

func main() {
	// Log service version.
	fmt.Println("FASTLY_SERVICE_VERSION:", os.Getenv("FASTLY_SERVICE_VERSION"))

	fsthttp.ServeFunc(func(ctx context.Context, w fsthttp.ResponseWriter, r *fsthttp.Request) {

		if strings.HasPrefix(r.URL.Path, "/mcp") {
			// Modern Streamable HTTP endpoint (2025-03-26 spec compliant)
			if r.Method == "POST" {
				hooks := &server.Hooks{}
				hooks.AddBeforeAny(func(ctx context.Context, id any, method mcp.MCPMethod, message any) {
					if method != mcp.MethodInitialize {
						fmt.Printf("mcp-session-id: %s", r.Header.Get("Mcp-Session-Id"))
						if r.Header.Get("Mcp-Session-Id") == "" {
							w.WriteHeader(fsthttp.StatusBadRequest)
							fmt.Fprintln(w, "Bad request") // The HTTP response body MAY comprise a JSON-RPC error response that has no id.
							return
						}
						rc, err := simple.Get([]byte(r.Header.Get("Mcp-Session-Id")))
						if err != nil {
							w.WriteHeader(fsthttp.StatusNotFound)
							fmt.Fprintln(w, "Not found")
							return
						} else {
							defer rc.Close()
						}
					}
				})
				hooks.AddAfterInitialize(func(ctx context.Context, id any, message *mcp.InitializeRequest, result *mcp.InitializeResult) {
					key := uuid.New().String()
					rc, err := simple.GetOrSet([]byte(key), func() (simple.CacheEntry, error) {
						return simple.CacheEntry{
							Body: bytes.NewReader([]byte("")), // store empty string as cache content won't be used for now
							TTL:  3 * time.Minute,
						}, nil
					})
					if err != nil {
						fsthttp.Error(w, err.Error(), fsthttp.StatusInternalServerError)
						return
					}
					defer rc.Close()
					w.Header().Set("Mcp-Session-Id", key)
				})

				reqBody, err := io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(fsthttp.StatusBadRequest)
					fmt.Fprintf(w, "Failed to read body.\n")
					return
				}
				var rawMessage json.RawMessage
				if err := json.Unmarshal(reqBody, &rawMessage); err != nil {
					fmt.Println(err)
				}
				mcpServer := NewMCPServer(hooks)
				response := mcpServer.HandleMessage(ctx, rawMessage)
				eventData, _ := json.Marshal(response)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(fsthttp.StatusOK)
				fmt.Fprintf(w, string(eventData))
			} else if r.Method == "DELETE" {
				if r.Header.Get("Mcp-Session-Id") == "" {
					w.WriteHeader(fsthttp.StatusBadRequest)
					fmt.Fprintln(w, "Bad request") // The HTTP response body MAY comprise a JSON-RPC error response that has no id.
				}
				if err := simple.Purge([]byte(r.Header.Get("Mcp-Session-Id")), simple.PurgeOptions{}); err != nil {
					w.WriteHeader(fsthttp.StatusMethodNotAllowed)
					fmt.Fprintln(w, "Not allowed")
				}
			} else {
				w.WriteHeader(fsthttp.StatusOK)
				fmt.Fprintf(w, "sorry - not supported request atm")
			}

		} else if strings.HasPrefix(r.URL.Path, "/sse") && r.Method == "GET" {
			// Legacy SSE endpoint for older clients
			sessionID := uuid.New().String()
			if len(r.Header.Get("Grip-Sig")) > 0 {
				// Request is from Fanout, handle it here
				m := map[string]GripResponse{
					"/sse": {"text/event-stream", "stream", sessionID},
				}
				if val, ok := m[r.URL.Path]; ok {
					w.Header().Set("Content-Type", val.contentType)
					w.Header().Set("Grip-Hold", val.gripHold)
					w.Header().Set("Grip-Channel", val.gripChannel)
					w.Header().Set("Cache-Control", "no-cache")
					w.Header().Set("Connection", "keep-alive")
					w.Header().Set("Access-Control-Allow-Origin", "*")
					fmt.Fprintf(w, "event: endpoint\ndata: /messages?sessionId=%s\r\n\r\n", sessionID)
					return

				} else {
					w.WriteHeader(fsthttp.StatusNotFound)
					fmt.Fprintf(w, "No such endpoint\n")
					return

				}

			} else {
				// Not from Fanout, route it through Fanout first
				handoff.Fanout("self")
				return
			}

		} else if strings.HasPrefix(r.URL.Path, "/messages") && r.Method == "POST" {
			// Publish message through Fanout
			queryStrings, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil || len(queryStrings["sessionId"]) == 0 {
				log.Printf("Something wrong with /POST: %s %s", err, queryStrings["sessionId"])
				return
			}
			sessionID := queryStrings["sessionId"][0]
			reqBody, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(fsthttp.StatusBadRequest)
				fmt.Fprintf(w, "Failed to read body.\n")
				return
			}
			var rawMessage json.RawMessage
			if err := json.Unmarshal(reqBody, &rawMessage); err != nil {
				fmt.Println(err)
			}
			mcpServer := NewMCPServer(&server.Hooks{})
			response := mcpServer.HandleMessage(ctx, rawMessage)
			if response != nil {
				eventData, _ := json.Marshal(response)
				message := PublishData{
					Items: []PublishItem{
						{
							Channel: sessionID,
							Formats: MessageData{
								HttpStream: HttpStreamData{
									Content: fmt.Sprintf("event: message\ndata: %s\n\n", eventData),
								},
							},
						},
					},
				}
				v, err := json.Marshal(message)
				if err != nil {
					fmt.Println(err)
				}

				req, err := fsthttp.NewRequest(fsthttp.MethodPost, fmt.Sprintf("https://api.fastly.com/service/%s/publish/", os.Getenv("FASTLY_SERVICE_ID")), bytes.NewReader(v))
				if err != nil {
					log.Printf("%s: create request: %v", req.URL, err)
					return
				}
				// TODO: Get FASTLY API TOKEN from Secret Store?
				req.Header.Set("Fastly-Key", "__PUT_YOUR_FASTLY_API_TOKEN__")
				fastResp, err := req.Send(ctx, "fastly")
				if err != nil {
					log.Printf("%s: send request: %v", req.URL, err)
					return
				}
				_, err = io.ReadAll(fastResp.Body)
				if err != nil {
					fmt.Fprintf(w, "Failed to read body.\n")
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(response)
				return
			}
		}
		return
	})
}
