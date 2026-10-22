// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The runtime image is alpine with ca-certificates and ffmpeg and no zoneinfo.
// Each group's cutoff is computed in its own zone, so the binary has to carry
// the zone database (review B1: it did not, and every group ran in UTC).
// Tests on a developer's machine pass either way, which is why this looks at
// what the binary links.
func TestBinaryEmbedsTzdata(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, p := range strings.Fields(string(out)) {
		if p == "time/tzdata" {
			return
		}
	}
	t.Fatal("cmd/bot does not link time/tzdata: time.LoadLocation would depend on the host's zoneinfo")
}
