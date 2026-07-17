package packages

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// NodeIndexURL is nodejs.org's release index — the same data nvm itself reads.
// Node.js versions are enumerated from here rather than from `nvm ls-remote`
// because nvm only exists after the first Node.js install: asking nvm what is
// installable would leave a fresh node with an empty list and no way to fill it.
const NodeIndexURL = "https://nodejs.org/dist/index.json"

// nodeRelease is one published Node.js release. LTS is `false` for a
// non-LTS release and the LTS codename (e.g. "Jod") otherwise, so it is decoded
// loosely and only its stringness is meaningful.
type nodeRelease struct {
	Version string          `json:"version"`
	LTS     json.RawMessage `json:"lts"`
}

// nodeLTSVersions returns the newest maintained LTS majors, newest last. The
// index lists every LTS line ever published (4, 6, 8, ...), so the newest
// nodeLTSCount are taken rather than all of them: a major enters when it reaches
// LTS and the oldest drops off, with no version literal to maintain.
//
// A failure is deliberately not fatal. The rest of the catalog comes from the
// local apt index and stays installable on a node that cannot reach nodejs.org;
// installing Node.js would need that network anyway.
func (o *HostOperator) nodeLTSVersions(ctx context.Context) []string {
	releases, err := o.fetchNodeIndex(ctx)
	if err != nil {
		return nil
	}
	seen := map[int]struct{}{}
	majors := []int{}
	for _, release := range releases {
		if !isLTS(release.LTS) {
			continue
		}
		major, ok := nodeMajorOf(release.Version)
		if !ok {
			continue
		}
		if _, exists := seen[major]; exists {
			continue
		}
		seen[major] = struct{}{}
		majors = append(majors, major)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(majors)))
	if len(majors) > nodeLTSCount {
		majors = majors[:nodeLTSCount]
	}
	sort.Ints(majors)
	versions := make([]string, 0, len(majors))
	for _, major := range majors {
		versions = append(versions, strconv.Itoa(major))
	}
	return versions
}

func (o *HostOperator) fetchNodeIndex(ctx context.Context) ([]nodeRelease, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, o.nodeIndexURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := o.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errUnexpectedNodeIndex(response.StatusCode)
	}
	var releases []nodeRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(&releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// isLTS reports whether a release's "lts" field is a codename rather than false.
func isLTS(raw json.RawMessage) bool {
	return len(raw) > 0 && strings.HasPrefix(strings.TrimSpace(string(raw)), `"`)
}

// nodeMajorOf extracts the major from a "vN.N.N" release tag, rejecting anything
// that is not a plain number so only a validated major reaches nvm.
func nodeMajorOf(version string) (int, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if dot := strings.Index(trimmed, "."); dot > 0 {
		trimmed = trimmed[:dot]
	}
	if !majorPattern.MatchString(trimmed) {
		return 0, false
	}
	major, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, false
	}
	return major, true
}
