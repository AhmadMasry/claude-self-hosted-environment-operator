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

package controller

import (
	"testing"
	"time"
)

func TestTickNilClockUsesWallTime(t *testing.T) {
	if d := time.Since(tick(nil)); d < -time.Second || d > time.Second {
		t.Fatalf("tick(nil) is %v away from time.Now()", d)
	}
}

func TestTickUsesTheClock(t *testing.T) {
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := tick(func() time.Time { return want }); !got.Equal(want) {
		t.Fatalf("tick = %v, want %v", got, want)
	}
}
