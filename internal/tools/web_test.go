package tools

import (
	"testing"
)

func TestWebSearchSchemaHasMode(t *testing.T) {
	props, ok := webSearchSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("webSearchSchema properties missing")
	}
	mode, ok := props["mode"]
	if !ok {
		t.Fatal("webSearchSchema missing mode property")
	}
	modeProps, ok := mode.(map[string]interface{})
	if !ok {
		t.Fatal("mode property not a map")
	}
	if modeProps["type"] != "string" {
		t.Fatalf("mode type should be string, got %v", modeProps["type"])
	}
	enum, ok := modeProps["enum"].([]string)
	if !ok || len(enum) != 3 {
		t.Fatalf("mode enum should have 3 values")
	}
	expected := []string{"small", "full", "raw"}
	for i, v := range expected {
		if enum[i] != v {
			t.Fatalf("enum[%d] expected %s, got %s", i, v, enum[i])
		}
	}
}

func TestWebSearchSchemaHasQuery(t *testing.T) {
	props, ok := webSearchSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("webSearchSchema properties missing")
	}
	query, ok := props["query"]
	if !ok {
		t.Fatal("webSearchSchema missing query property")
	}
	queryProps, ok := query.(map[string]interface{})
	if !ok {
		t.Fatal("query property not a map")
	}
	if queryProps["type"] != "string" {
		t.Fatalf("query type should be string, got %v", queryProps["type"])
	}
}

func TestWebSearchSchemaRequired(t *testing.T) {
	required, ok := webSearchSchema["required"].([]string)
	if !ok {
		t.Fatal("webSearchSchema required missing")
	}
	found := false
	for _, r := range required {
		if r == "query" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("query should be required")
	}
}
