package ani

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

// ---------------------------------------------------------------------------
// C10: the locked chart material the gate reads.
//
// kubekey/ani/charts/**/*.tgz is gitignored, so a clean checkout of this
// repository contains no charts at all, and
// TestChartSpecsMatchTheApprovedLockAndTheChartBytes — which exists precisely to
// keep the component spec table, the approved lock and the shipped archive bytes
// from drifting apart again — fails on CI for want of files. That is a missing
// input, not a wrong assertion: skipping or weakening the test would delete the
// only check that caught the cert-manager "1.21.2" vs "v1.21.2" defect.
//
// So the preparation step is explicit and separate: fetch each chart from the
// source the lock names, verify it against the full sha256 the lock approves,
// and put it where the gate looks. Network happens here and nowhere else; the
// gate itself stays offline and fails loudly if the material is absent.
// ---------------------------------------------------------------------------

// MaterialsPrepareChartsInput describes one chart-preparation run.
type MaterialsPrepareChartsInput struct {
	// LockPath is the approved materials lock. It is the only authority on what
	// a chart must be: its source, its digest, its name and its version.
	LockPath string
	// Root is the source-tree ani/ directory the gate reads from.
	Root string
	// CacheDir holds the content-addressed download cache. Re-running is cheap,
	// but a cache hit is still re-verified before it is adopted.
	CacheDir string
	// Offline refuses any download. Bytes already present are still verified;
	// anything missing is an error naming what is missing.
	Offline bool
	// Client is the test seam. Nil means a bounded, timeout-carrying client.
	Client *http.Client
}

// offlineMissing marks the one refusal that is worth collecting across charts:
// a run with --offline reports every chart it could not satisfy at once, instead
// of the operator re-running once per missing file to discover them one by one.
var offlineMissing = errors.New("not present offline")

// ctxWithTimeout derives the request context from the client's own bound, so the
// HTTP timeout stays stated in exactly one place.
func ctxWithTimeout(client *http.Client) context.Context {
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return context.WithoutCancel(ctx)
}

// hashBytes digests an in-memory body with the same function the streaming paths
// use, so no second implementation can disagree about what "the digest" means.
func hashBytes(body []byte) string {
	digest, err := hashStream(bytes.NewReader(body))
	if err != nil {
		// hashStream only fails when the writer fails, and a string reader
		// cannot. Reporting a failure here would be a lie, so this panics loudly
		// rather than return a digest that silently is not one.
		panic(errors.Wrap(err, "hash an in-memory chart body"))
	}
	return digest
}

// maxChartArchiveBytes bounds one download. Approved charts are tens of KB to a
// few MB; an unbounded reader turns a misbehaving endpoint into an exhausted disk.
const maxChartArchiveBytes = 64 << 20

// RunMaterialsPrepareCharts lands every chart the lock approves, at the
// source-tree path the gate opens, and verifies each one twice over: by digest
// and by the identity the archive declares about itself.
func RunMaterialsPrepareCharts(input MaterialsPrepareChartsInput, stdout io.Writer) error {
	out := materialWriter(stdout)
	lock, err := LoadMaterialsLock(input.LockPath)
	if err != nil {
		return err
	}
	if len(lock.Charts) == 0 {
		return errors.New("the approved lock carries no charts; there is nothing to prepare")
	}
	if strings.TrimSpace(input.Root) == "" {
		return errors.New("--root is required")
	}
	// One HTTP client for the whole run so the timeout is stated once.
	client := input.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	type outcome struct {
		name   string
		status string
	}
	outcomes := make([]outcome, 0, len(lock.Charts))
	var missing []string
	for _, chart := range lock.Charts {
		where := fmt.Sprintf("lock chart %s %s", chart.Name, chart.ChartVersion)
		digest := strings.ToLower(strings.TrimPrefix(chart.SHA256, "sha256:"))
		if !isHex64(digest) {
			return fmt.Errorf("%s: the approved sha256 %q is not 64 hex characters", where, chart.SHA256)
		}
		// The destination is built from the entry's own name and version, so those
		// two fields are the path input that has to be constrained. They are
		// checked as single tokens BEFORE any join, because filepath.Join cleans
		// as it goes: "../evil" would otherwise fold into a still-relative path
		// and land somewhere inside the root that is not this chart's directory.
		relative, err := chartRelativePath(chart)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		destination, err := safeArtifactPath(input.Root, relative)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if inside, err := filepath.Rel(filepath.Join(input.Root, "charts", chart.Name), destination); err != nil ||
			filepath.IsAbs(inside) || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%s: the prepared path %s does not stay inside charts/%s", where, destination, chart.Name)
		}
		status, err := prepareOneChart(ctxWithTimeout(client), input, chart, digest, destination, client)
		if err != nil {
			if errors.Is(err, offlineMissing) {
				missing = append(missing, fmt.Sprintf("%s %s", chart.Name, chart.ChartVersion))
				continue
			}
			return errors.Wrapf(err, "%s", where)
		}
		fmt.Fprintf(out, "chart %s %s: %s\n", chart.Name, chart.ChartVersion, status)
		outcomes = append(outcomes, outcome{name: chart.Name + " " + chart.ChartVersion, status: status})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("--offline cannot prepare %d chart(s) from local bytes (%s); run the preparation step with network access",
			len(missing), strings.Join(missing, ", "))
	}
	fmt.Fprintf(out, "charts prepared: %d/%d (lock %s)\n", len(outcomes), len(lock.Charts), input.LockPath)
	return nil
}

// prepareOneChart resolves one lock entry to verified bytes at destination and
// returns how it got there, for the log line.
func prepareOneChart(ctx context.Context, input MaterialsPrepareChartsInput, chart LockedChart,
	digest, destination string, client *http.Client) (string, error) {
	// An existing destination is only ever kept after its bytes agree with this
	// entry. A mismatch is reported, never quietly overwritten: on a developer
	// tree that already has material, silently re-downloading would hide the fact
	// that what is on disk is not what the lock approves.
	switch at, err := classify(destination, digest); {
	case err != nil:
		return "", err
	case at == verifiedPresent:
		if err := verifyChartIdentity(destination, chart); err != nil {
			return "", err
		}
		return "already present (digest and Chart.yaml verified)", nil
	case at == mismatchedOnDisk:
		return "", fmt.Errorf("%s %s already exists at %s and hashes to something other than the approved %s; remove it deliberately rather than let the fetch overwrite it",
			chart.Name, chart.ChartVersion, destination, digest)
	}
	cached := filepath.Join(input.CacheDir, "sha256", digest+".tgz")
	switch at, err := classify(cached, digest); {
	case err != nil:
		return "", err
	case at == mismatchedOnDisk:
		// A cache entry that does not match its own file name is corrupt, and
		// adopting it would launder bad bytes into a passing gate.
		return "", fmt.Errorf("cache entry %s hashes to something other than the digest it is stored under", cached)
	case at == verifiedPresent:
		if err := verifyChartIdentity(cached, chart); err != nil {
			return "", err
		}
		if err := copyVerifiedFile(cached, destination); err != nil {
			return "", err
		}
		return "from cache (re-verified)", nil
	}
	if input.Offline {
		return "", errors.Wrapf(offlineMissing, "%s %s is not present locally with the approved digest %s",
			chart.Name, chart.ChartVersion, digest)
	}
	if strings.TrimSpace(chart.Source) == "" {
		return "", fmt.Errorf("the lock approves no source for %s %s, so its bytes cannot be prepared", chart.Name, chart.ChartVersion)
	}
	if !strings.HasPrefix(chart.Source, "https://") {
		return "", fmt.Errorf("the lock source %q for %s %s is not https", chart.Source, chart.Name, chart.ChartVersion)
	}
	if err := downloadChart(ctx, client, chart.Source, digest, cached); err != nil {
		return "", err
	}
	if err := verifyChartIdentity(cached, chart); err != nil {
		return "", err
	}
	if err := copyVerifiedFile(cached, destination); err != nil {
		return "", err
	}
	return "downloaded and verified", nil
}

// chartRelativePath names where one approved chart belongs in the source tree,
// refusing anything that is not a single safe path token first.
func chartRelativePath(chart LockedChart) (string, error) {
	for _, token := range []struct{ field, value string }{
		{"name", chart.Name}, {"chartVersion", chart.ChartVersion},
	} {
		if token.value == "" {
			return "", fmt.Errorf("the lock entry has an empty %s", token.field)
		}
		if token.value != filepath.Base(token.value) || token.value == "." || token.value == ".." ||
			strings.ContainsAny(token.value, `\/`) || strings.Contains(token.value, "..") {
			return "", fmt.Errorf("the lock %s %q is not a single safe path element", token.field, token.value)
		}
	}
	return "charts/" + chart.Name + "/" + chart.Name + "-" + chart.ChartVersion + ".tgz", nil
}

const (
	verifiedPresent  = "present"
	mismatchedOnDisk = "mismatched"
	absent           = "absent"
)

// classify reports whether a path holds exactly the approved bytes. A missing
// file is a third, ordinary state — the caller must be able to tell "not there
// yet" apart from "there, and wrong". An existing but unreadable file is an
// error, never a silent miss.
func classify(path, digest string) (string, error) {
	actual, err := fileDigestHex(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return absent, nil
	case err != nil:
		return "", err
	case actual != digest:
		return mismatchedOnDisk, nil
	}
	return verifiedPresent, nil
}

// verifyChartIdentity checks the archive says which chart it is. Bytes matching a
// digest is not enough: RunMaterialsPlaceCharts refuses an archive that declares
// a different chart, and the source tree must not be the one place that rule is
// relaxed.
func verifyChartIdentity(path string, chart LockedChart) error {
	name, version, err := readChartIdentity(path)
	if err != nil {
		return err
	}
	if name != chart.Name || version != chart.ChartVersion {
		return fmt.Errorf("%s %s at %s declares itself %s %s", chart.Name, chart.ChartVersion, path, name, version)
	}
	return nil
}

// downloadChart writes the response to a unique temp file, flushes it, verifies
// the digest, and only then renames it into the cache under the digest it proved.
func downloadChart(ctx context.Context, client *http.Client, url, digest, destination string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.Wrapf(err, "request %s", url)
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.Wrapf(err, "fetch %s", url)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s returned %s, not 200 OK", url, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxChartArchiveBytes+1))
	if err != nil {
		return errors.Wrapf(err, "read %s", url)
	}
	if len(body) == 0 {
		return fmt.Errorf("fetch %s returned an empty body", url)
	}
	if len(body) > maxChartArchiveBytes {
		return fmt.Errorf("fetch %s exceeded the %d byte chart size limit", url, maxChartArchiveBytes)
	}
	actual := hashBytes(body)
	if actual != digest {
		return fmt.Errorf("fetch %s hashes to %s, not the approved %s; the bytes are refused, not adopted", url, actual, digest)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return errors.Wrapf(err, "create the chart cache directory")
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".chart-*.part")
	if err != nil {
		return errors.Wrap(err, "create a temporary chart file")
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return errors.Wrapf(err, "write %s", tempPath)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return errors.Wrapf(err, "flush %s", tempPath)
	}
	if err := temp.Close(); err != nil {
		return errors.Wrapf(err, "close %s", tempPath)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return errors.Wrapf(err, "install the verified chart into %s", destination)
	}
	// Re-read through the final path: the digest above proved the bytes in
	// memory, this proves what is now on disk under them.
	return requireDigest(destination, digest)
}

// copyVerifiedFile places cache bytes at their destination without ever leaving
// a half-written archive where the gate would read it.
func copyVerifiedFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return errors.Wrapf(err, "create the chart directory")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return errors.Wrapf(err, "read the verified cache chart %s", source)
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".chart-*.part")
	if err != nil {
		return errors.Wrap(err, "create a temporary chart file")
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return errors.Wrapf(err, "write %s", tempPath)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return errors.Wrapf(err, "flush %s", tempPath)
	}
	if err := temp.Close(); err != nil {
		return errors.Wrapf(err, "close %s", tempPath)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return errors.Wrapf(err, "place the chart at %s", destination)
	}
	return nil
}

// requireDigest re-reads a landed file and refuses it unless it is the approved
// material.
func requireDigest(path, digest string) error {
	actual, err := fileDigestHex(path)
	if err != nil {
		return err
	}
	if actual != digest {
		return fmt.Errorf("%s hashes to %s after being placed, not the approved %s", path, actual, digest)
	}
	return nil
}
