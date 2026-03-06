package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseICEURLs(t *testing.T) {
	raw := " turn:relay.example.com:3478?transport=udp, turns:relay.example.com:5349?transport=tcp , ,turn:relay.example.com:3478?transport=tcp "
	want := []string{
		"turn:relay.example.com:3478?transport=udp",
		"turns:relay.example.com:5349?transport=tcp",
		"turn:relay.example.com:3478?transport=tcp",
	}

	if got := parseICEURLs(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseICEURLs() = %#v, want %#v", got, want)
	}
}

func TestBuildICEConfigurationIncludesTURNURLs(t *testing.T) {
	turnURLs := []string{
		"turn:relay.example.com:3478?transport=udp",
		"turns:relay.example.com:5349?transport=tcp",
	}
	config := buildICEConfiguration(turnURLs, "alice", "secret")

	if len(config.ICEServers) != 2 {
		t.Fatalf("expected 2 ICE servers, got %d", len(config.ICEServers))
	}
	if !reflect.DeepEqual(config.ICEServers[1].URLs, turnURLs) {
		t.Fatalf("TURN URLs = %#v, want %#v", config.ICEServers[1].URLs, turnURLs)
	}
	if config.ICEServers[1].Username != "alice" {
		t.Fatalf("TURN username = %q, want %q", config.ICEServers[1].Username, "alice")
	}
	if config.ICEServers[1].Credential != "secret" {
		t.Fatalf("TURN credential = %v, want %q", config.ICEServers[1].Credential, "secret")
	}
}

func TestMarshalClientICEConfigPreservesSpecialCharacters(t *testing.T) {
	data, err := marshalClientICEConfig(
		[]string{"turns:relay.example.com:5349?transport=tcp"},
		"user'name",
		"pa\"ss",
	)
	if err != nil {
		t.Fatalf("marshalClientICEConfig() error = %v", err)
	}

	var config clientICEConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(config.ICEServers) != 2 {
		t.Fatalf("expected 2 ICE servers, got %d", len(config.ICEServers))
	}
	if config.ICEServers[1].Username != "user'name" {
		t.Fatalf("username = %q, want %q", config.ICEServers[1].Username, "user'name")
	}
	if config.ICEServers[1].Credential != "pa\"ss" {
		t.Fatalf("credential = %q, want %q", config.ICEServers[1].Credential, "pa\"ss")
	}
}
