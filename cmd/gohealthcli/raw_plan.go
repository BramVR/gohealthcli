package main

import (
	"encoding/json"
	"io"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

type rawPlanTarget struct {
	Kind         string `json:"kind"`
	EndpointName string `json:"endpoint_name"`
	DataType     string `json:"data_type,omitempty"`
	SourceFamily string `json:"source_family,omitempty"`
}

type rawPlanRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type rawPlanRange struct {
	From       string `json:"from"`
	To         string `json:"to,omitempty"`
	Timezone   string `json:"timezone"`
	ResolvedAt string `json:"resolved_at"`
}

type rawPlanPaging struct {
	PageSize          int64 `json:"page_size"`
	PageTokenProvided bool  `json:"page_token_provided"`
}

type rawPlanEffects struct {
	ProviderRequest     bool `json:"provider_request"`
	CredentialStoreRead bool `json:"credential_store_read"`
	TokenLoad           bool `json:"token_load"`
	TokenRefresh        bool `json:"token_refresh"`
	HealthArchiveOpen   bool `json:"health_archive_open"`
	HealthArchiveWrite  bool `json:"health_archive_write"`
	Migration           bool `json:"migration"`
	CursorChange        bool `json:"cursor_change"`
	SidecarCreation     bool `json:"sidecar_creation"`
}

type rawPlanResult struct {
	Status          string         `json:"status"`
	Target          rawPlanTarget  `json:"target"`
	Request         rawPlanRequest `json:"request"`
	RequiredScopes  []string       `json:"required_scopes"`
	Range           *rawPlanRange  `json:"range,omitempty"`
	Paging          rawPlanPaging  `json:"paging"`
	PlanningEffects rawPlanEffects `json:"planning_effects"`
	Message         string         `json:"message"`
}

func writeRawPlan(description googlehealth.RawRequestDescription, options googlehealth.RawRequestOptions, mode outputMode, stdout, stderr io.Writer) int {
	result := newRawPlanResult(description, options)
	var err error
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(result)
	} else {
		writer := newStickyWriter(stdout)
		if mode.plain {
			writeRawPlanPlain(writer, result)
		} else {
			writeRawPlanHuman(writer, result)
		}
		err = writer.Err()
	}
	if err != nil {
		return reportWriteFailure("raw", err, mode, stdout, stderr)
	}
	return 0
}

func newRawPlanResult(description googlehealth.RawRequestDescription, options googlehealth.RawRequestOptions) rawPlanResult {
	request := description.Request
	result := rawPlanResult{
		Status: "plan_ready",
		Target: rawPlanTarget{
			Kind:         options.Target[0],
			EndpointName: request.EndpointName,
			DataType:     request.DataType,
			SourceFamily: request.SourceFamilyFilter,
		},
		Request: rawPlanRequest{
			Method:  request.Method,
			URL:     description.SanitizedURL,
			Headers: description.Headers,
		},
		RequiredScopes: append([]string(nil), request.RequiredScopes...),
		Paging: rawPlanPaging{
			PageSize:          description.PageSize,
			PageTokenProvided: description.PageTokenProvided,
		},
		PlanningEffects: rawPlanEffects{},
		Message:         "Raw request plan is ready; no external access was performed.",
	}
	if description.Range != nil {
		result.Range = &rawPlanRange{
			From:       description.Range.From,
			To:         description.Range.To,
			Timezone:   description.Range.Timezone,
			ResolvedAt: description.Range.ResolvedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	return result
}

func writeRawPlanPlain(writer *stickyWriter, result rawPlanResult) {
	writer.Printf("status: %s\n", result.Status)
	writer.Printf("target.kind: %s\n", result.Target.Kind)
	writer.Printf("target.endpoint_name: %s\n", result.Target.EndpointName)
	if result.Target.DataType != "" {
		writer.Printf("target.data_type: %s\n", result.Target.DataType)
	}
	if result.Target.SourceFamily != "" {
		writer.Printf("target.source_family: %s\n", result.Target.SourceFamily)
	}
	writer.Printf("request.method: %s\n", result.Request.Method)
	writer.Printf("request.url: %s\n", result.Request.URL)
	for _, name := range []string{"Accept", "Content-Type"} {
		if value := result.Request.Headers[name]; value != "" {
			writer.Printf("request.headers.%s: %s\n", name, value)
		}
	}
	for index, scope := range result.RequiredScopes {
		writer.Printf("required_scopes.%d: %s\n", index, scope)
	}
	if result.Range != nil {
		writer.Printf("range.from: %s\n", result.Range.From)
		if result.Range.To != "" {
			writer.Printf("range.to: %s\n", result.Range.To)
		}
		writer.Printf("range.timezone: %s\n", result.Range.Timezone)
		writer.Printf("range.resolved_at: %s\n", result.Range.ResolvedAt)
	}
	writer.Printf("paging.page_size: %d\n", result.Paging.PageSize)
	writer.Printf("paging.page_token_provided: %t\n", result.Paging.PageTokenProvided)
	writeRawPlanEffects(writer, "planning_effects.", result.PlanningEffects)
	writer.Printf("message: %s\n", result.Message)
}

func writeRawPlanHuman(writer *stickyWriter, result rawPlanResult) {
	writer.Printf("Raw request plan: %s\n", result.Status)
	writer.Printf("Target: %s (%s)\n", result.Target.EndpointName, result.Target.Kind)
	if result.Target.SourceFamily != "" {
		writer.Printf("Source family: %s\n", result.Target.SourceFamily)
	}
	writer.Printf("Request: %s %s\n", result.Request.Method, result.Request.URL)
	writer.Printf("Headers: Accept=%s\n", result.Request.Headers["Accept"])
	writer.Printf("Required scopes: %s\n", joinPlanValues(result.RequiredScopes))
	if result.Range != nil {
		if result.Range.To == "" {
			writer.Printf("Range: %s (no Provider upper bound)\n", result.Range.From)
		} else {
			writer.Printf("Range: %s to %s\n", result.Range.From, result.Range.To)
		}
		writer.Printf("Timezone: %s (resolved at %s)\n", result.Range.Timezone, result.Range.ResolvedAt)
	}
	writer.Printf("Paging: page size %d, page token provided %t\n", result.Paging.PageSize, result.Paging.PageTokenProvided)
	writer.Printf("Planning effects: all false\n")
	writer.Printf("%s\n", result.Message)
}

func writeRawPlanEffects(writer *stickyWriter, prefix string, effects rawPlanEffects) {
	writer.Printf("%sprovider_request: %t\n", prefix, effects.ProviderRequest)
	writer.Printf("%scredential_store_read: %t\n", prefix, effects.CredentialStoreRead)
	writer.Printf("%stoken_load: %t\n", prefix, effects.TokenLoad)
	writer.Printf("%stoken_refresh: %t\n", prefix, effects.TokenRefresh)
	writer.Printf("%shealth_archive_open: %t\n", prefix, effects.HealthArchiveOpen)
	writer.Printf("%shealth_archive_write: %t\n", prefix, effects.HealthArchiveWrite)
	writer.Printf("%smigration: %t\n", prefix, effects.Migration)
	writer.Printf("%scursor_change: %t\n", prefix, effects.CursorChange)
	writer.Printf("%ssidecar_creation: %t\n", prefix, effects.SidecarCreation)
}

func joinPlanValues(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	result := values[0]
	for _, value := range values[1:] {
		result += ", " + value
	}
	return result
}
