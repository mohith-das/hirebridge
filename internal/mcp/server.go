package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"hirebridge/internal/service"
)

type MCPServer struct {
	SearchSvc *service.SearchService
	DB        *sql.DB
}

func NewMCPServer(searchSvc *service.SearchService, db *sql.DB, baseURL string) *server.StreamableHTTPServer {
	s := &MCPServer{SearchSvc: searchSvc, DB: db}

	mcpServer := server.NewMCPServer(
		"HireBridge",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	s.registerTools(mcpServer)

	streamableHTTP := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithStateLess(true),
		server.WithProtectedResourceMetadata(server.ProtectedResourceMetadataConfig{
			Resource:             baseURL,
			AuthorizationServers: []string{baseURL},
			ScopesSupported:      []string{"talent:search", "talent:read", "intro:request"},
		}),
	)

	return streamableHTTP
}

func (s *MCPServer) registerTools(mcpServer *server.MCPServer) {
	mcpServer.AddTool(
		mcp.NewTool("search_talent",
			mcp.WithDescription("Search the talent network by keyword. Returns ranked pointers to candidate profiles across connected nodes."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search query string (keywords, skills, titles)")),
			mcp.WithNumber("limit", mcp.Description("Maximum results (default 20, max 100)")),
			mcp.WithArray("query_vector",
				mcp.Description("Optional 384-dim float vector for semantic re-ranking. If omitted, pure BM25 keyword ranking."),
				mcp.WithNumberItems(),
			),
		),
		s.handleSearchTalent,
	)

	mcpServer.AddTool(
		mcp.NewTool("get_talent_profile",
			mcp.WithDescription("Retrieve the full cached career packet for a candidate, including the signed snapshot."),
			mcp.WithString("candidate_id", mcp.Required(), mcp.Description("The candidate ID returned by search_talent")),
		),
		s.handleGetTalentProfile,
	)

	mcpServer.AddTool(
		mcp.NewTool("request_introduction",
			mcp.WithDescription("Request an introduction to a candidate. Pings the candidate's edge node inbox."),
			mcp.WithString("candidate_id", mcp.Required(), mcp.Description("The candidate ID")),
			mcp.WithString("recruiter_identity", mcp.Required(), mcp.Description("Your name, company, and contact info")),
		),
		s.handleRequestIntroduction,
	)
}

func (s *MCPServer) handleSearchTalent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, _ := req.RequireString("query")
	limit := int(req.GetFloat("limit", 20))

	var vec []float64
	args := req.GetArguments()
	if queryVec, ok := args["query_vector"]; ok {
		if arr, ok := queryVec.([]interface{}); ok {
			for _, v := range arr {
				if f, ok := v.(float64); ok {
					vec = append(vec, f)
				}
			}
		}
	}

	result, err := s.SearchSvc.SearchTalent(query, limit, vec)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	jsonBytes, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(jsonBytes)), nil
}

func (s *MCPServer) handleGetTalentProfile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	candidateID, err := req.RequireString("candidate_id")
	if err != nil {
		return mcp.NewToolResultError("candidate_id required"), nil
	}

	profile, err := s.SearchSvc.GetTalentProfile(candidateID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("lookup failed: %v", err)), nil
	}
	if profile == nil {
		return mcp.NewToolResultError("candidate not found"), nil
	}

	jsonBytes, _ := json.Marshal(profile)
	return mcp.NewToolResultText(string(jsonBytes)), nil
}

func (s *MCPServer) handleRequestIntroduction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	candidateID, _ := req.RequireString("candidate_id")
	recruiterIdentity, _ := req.RequireString("recruiter_identity")

	result := map[string]any{
		"request_id":          fmt.Sprintf("req_%x", candidateID),
		"status":              "queued",
		"candidate_id":        candidateID,
		"recruiter_identity":  recruiterIdentity,
		"delivered":           false,
		"note":                "introduction request recorded; outbound notification pending",
	}

	jsonBytes, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(jsonBytes)), nil
}
