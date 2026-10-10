// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"io"
	"strings"

	"google.golang.org/protobuf/proto"

	pb "github.com/polipan/polipan/proto/agentv1"
)

func stringsReader(s string) io.Reader { return strings.NewReader(s) }

// proto2test returns the target of a test. The test repository lives in a sub folder, so the real repository stays clean.
func proto2test(t *pb.BackupTargetSpec) *pb.BackupTargetSpec {
	if t == nil {
		return nil
	}
	c := proto.Clone(t).(*pb.BackupTargetSpec)
	c.Repository = testRepo(t.Repository)
	return c
}

// testRepo adds "/_polipan_test" to the path of the repository string of any kind.
func testRepo(repo string) string {
	return strings.TrimRight(repo, "/") + "/_polipan_test"
}
