package githubvar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/heartbeat"
)

type fixtureCase struct {
	Name            string `yaml:"name"`
	UpdateStatus    int    `yaml:"updateStatus"`
	CreateStatus    int    `yaml:"createStatus"`
	WantUpdateCalls int    `yaml:"wantUpdateCalls"`
	WantCreateCalls int    `yaml:"wantCreateCalls"`
	WantRecordJSON  bool   `yaml:"wantRecordJSON"`
	WantErr         bool   `yaml:"wantErr"`
}

type tokenStub struct{}

func (tokenStub) Token(context.Context) (string, error) { return "test-token", nil }

func loadFixture(t *testing.T) []fixtureCase {
	t.Helper()
	data, err := os.ReadFile("testdata/publisher.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var cases []fixtureCase
	if err := yaml.Unmarshal(data, &cases); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("fixture contains no cases")
	}
	return cases
}

func TestPublish(t *testing.T) {
	fixed := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range loadFixture(t) {
		t.Run(tc.Name, func(t *testing.T) {
			var updateCalls, createCalls int
			var gotAuth, gotBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/actions/variables/HEALTH":
					updateCalls++
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/actions/variables":
					createCalls++
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				gotAuth = r.Header.Get("Authorization")
				body, _ := io.ReadAll(r.Body)
				gotBody = string(body)
				switch {
				case r.Method == http.MethodPatch:
					status := tc.UpdateStatus
					if status == 0 {
						status = http.StatusNoContent
					}
					w.WriteHeader(status)
				default:
					status := tc.CreateStatus
					if status == 0 {
						status = http.StatusCreated
					}
					w.WriteHeader(status)
				}
				_, _ = w.Write([]byte("{}"))
			}))
			defer server.Close()

			publisher, err := New(Config{
				Repo:       "o/r",
				Variable:   "HEALTH",
				Tokens:     tokenStub{},
				BaseURL:    server.URL,
				HTTPClient: server.Client(),
				Now:        func() time.Time { return fixed },
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			err = publisher.Publish(context.Background(), heartbeat.Record{
				ListenerHealthy: true,
				AssignedJobs:    3,
				RunningJobs:     2,
				LiveRunners:     2,
			})
			if tc.WantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Publish: %v", err)
			}
			if updateCalls != tc.WantUpdateCalls {
				t.Fatalf("update calls = %d, want %d", updateCalls, tc.WantUpdateCalls)
			}
			if createCalls != tc.WantCreateCalls {
				t.Fatalf("create calls = %d, want %d", createCalls, tc.WantCreateCalls)
			}
			if gotAuth != "Bearer test-token" {
				t.Fatalf("authorization = %q", gotAuth)
			}
			if tc.WantRecordJSON {
				var body struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				}
				if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				if body.Name != "HEALTH" {
					t.Fatalf("body name = %q", body.Name)
				}
				var record heartbeat.Record
				if err := json.Unmarshal([]byte(body.Value), &record); err != nil {
					t.Fatalf("decode record: %v", err)
				}
				if !record.ListenerHealthy || record.AssignedJobs != 3 || record.RunningJobs != 2 || record.LiveRunners != 2 {
					t.Fatalf("record = %+v", record)
				}
				if !record.Timestamp.Equal(fixed) {
					t.Fatalf("timestamp = %v, want %v", record.Timestamp, fixed)
				}
			}
		})
	}
}
