/*
Copyright 2026 Ahmad Masry.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hook

import (
	"encoding/json"
	"io"
	"regexp"
	"time"
)

// sensitive matches emails, JWTs and Anthropic-issued credentials so an API
// error message can never carry them into the orchestrator log.
var sensitive = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}|eyJ[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+){1,2}|sk-ant-[A-Za-z0-9_-]+|ccenvkey_[A-Za-z0-9_-]+`)

type logLine struct {
	Time       string `json:"ts"`
	OrderID    string `json:"orderID"`
	SessionID  string `json:"sessionID,omitempty"`
	Attempt    int32  `json:"attempt"`
	Outcome    string `json:"outcome"`
	ExitCode   int    `json:"exitCode"`
	RunnerName string `json:"runner,omitempty"`
	Warning    string `json:"warning,omitempty"`
	Error      string `json:"error,omitempty"`
}

// WriteLog emits the hook's single structured log line.
func WriteLog(w io.Writer, in Input, res Result) {
	l := logLine{Time: time.Now().UTC().Format(time.RFC3339), OrderID: in.OrderID, SessionID: in.SessionID,
		Attempt: in.Attempt, Outcome: res.Outcome, ExitCode: res.ExitCode, RunnerName: res.RunnerName,
		Warning: Redact(res.Warning)}
	if res.Err != nil {
		l.Error = Redact(res.Err.Error())
	}
	b, _ := json.Marshal(l)
	_, _ = w.Write(append(b, '\n'))
}

// Redact replaces credential-shaped substrings with a marker.
func Redact(s string) string {
	return sensitive.ReplaceAllString(s, "[redacted]")
}
