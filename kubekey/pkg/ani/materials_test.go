/*
Copyright 2026 The KubeSphere Contributors.

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

package ani

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	r07SourceDigest   = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	r07PlatformDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	r07AnotherDigest  = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

// r07ImageEntry builds one lock image entry with the given digest fields.
func r07ImageEntry(original, haulerRef, sourceDigest, platformDigest string) string {
	return "  - original: " + original + "\n" +
		"    haulerRef: " + haulerRef + "\n" +
		"    sourceManifestDigest: " + sourceDigest + "\n" +
		"    amd64ManifestDigest: " + platformDigest + "\n" +
		"    use: test entry\n"
}

const r07ImageSection = `verificationToolImages:
%s
`

func r07ParseSection(t *testing.T, section string) (*MaterialsLock, error) {
	t.Helper()
	text := "apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\n" + section
	return ParseMaterialsLock([]byte(text))
}

// The shipped lock must parse and validate as-is: every real entry is the
// baseline the negative tests mutate.
func TestMaterialsLockParsesShippedFile(t *testing.T) {
	path := filepath.Join("..", "..", "ani", "components.lock.yaml")
	lock, err := LoadMaterialsLock(path)
	if err != nil {
		t.Fatalf("the shipped materials lock must parse: %v", err)
	}
	if len(lock.Images) < 20 {
		t.Fatalf("the shipped lock yielded %d images, want at least 20", len(lock.Images))
	}
	if len(lock.Charts) < 5 {
		t.Fatalf("the shipped lock yielded %d charts, want at least 5", len(lock.Charts))
	}
	if len(lock.Tools) != 1 {
		t.Fatalf("the shipped lock yielded %d tools, want 1 (helm)", len(lock.Tools))
	}
	chart, ok := lock.ChartByArtifactPath("charts/cert-manager/v1.21.2.tgz")
	if !ok || chart.SHA256 == "" {
		t.Fatalf("charts/cert-manager/v1.21.2.tgz is not in the lock: %+v", chart)
	}
	tool, ok := lock.ToolByArtifactPath("bin/helm")
	if !ok || tool.BinarySHA256 == "" {
		t.Fatalf("bin/helm is not in the lock: %+v", tool)
	}
	seen := map[string]bool{}
	for _, image := range lock.Images {
		if seen[image.HaulerRef] {
			t.Fatalf("duplicate local reference %q in the shipped lock", image.HaulerRef)
		}
		seen[image.HaulerRef] = true
	}
}

// T-R07-03: the source index digest and the platform manifest digest describe
// different objects. A correct image passes with two different digests, each is
// verified against its own field, and neither is ever compared with the other.
func TestImageDigestRulesKeepSourceAndPlatformSeparate(t *testing.T) {
	entry := LockedImage{
		Original:             "quay.io/example/app:v1",
		HaulerRef:            "127.0.0.1:5000/example/app:v1",
		SourceManifestDigest: r07SourceDigest,
		PlatformDigest:       r07PlatformDigest,
		Platform:             InstallerPlatform,
	}
	if err := VerifyImageDigests(entry, r07SourceDigest, r07PlatformDigest); err != nil {
		t.Fatalf("correct digests must pass: %v", err)
	}
	if err := VerifyImageDigests(entry, r07SourceDigest, r07AnotherDigest); err == nil {
		t.Fatal("a wrong platform digest must fail")
	}
	err := VerifyImageDigests(entry, r07AnotherDigest, r07PlatformDigest)
	if err == nil {
		t.Fatal("a wrong source index digest must fail")
	}
	if !strings.Contains(err.Error(), "source index digest") {
		t.Fatalf("error = %v, want it to name the source index digest", err)
	}
	// Handing the platform digest as the source digest must fail: the two are
	// never compared with each other, so this cannot pass by accident.
	if err := VerifyImageDigests(entry, r07PlatformDigest, r07PlatformDigest); err == nil {
		t.Fatal("cross-submitting the platform digest as the source digest must fail")
	}
}

// T-R07-02 (pure-function part): digest formats, missing digests, duplicate
// originals and local reference conflicts must be rejected.
func TestMaterialsLockRejectsBrokenIdentities(t *testing.T) {
	cases := map[string]func(lock string) string{
		"digest too short": func(lock string) string {
			return strings.Replace(lock, r07PlatformDigest, "sha256:abcd", 1)
		},
		"digest uppercase hex": func(lock string) string {
			return strings.Replace(lock, "sha256:2222", "sha256:222A", 1)
		},
		"digest wrong algorithm": func(lock string) string {
			return strings.Replace(lock, r07SourceDigest, "sha1:1111111111111111111111111111111111111111", 1)
		},
		"missing platform digest": func(lock string) string {
			return strings.Replace(lock, "  amd64ManifestDigest: "+r07PlatformDigest+"\n", "", 1)
		},
		"unknown digest on an enabled image": func(lock string) string {
			return strings.Replace(lock, "  sourceManifestDigest: "+r07SourceDigest+"\n",
				"  sourceManifestDigest: null\n", 1)
		},
		"duplicate original": func(lock string) string {
			return lock + "  - original: quay.io/example/app:v1\n    haulerRef: 127.0.0.1:5000/example/app:v2\n    sourceManifestDigest: " + r07SourceDigest + "\n    amd64ManifestDigest: " + r07PlatformDigest + "\n"
		},
		"local reference conflict": func(lock string) string {
			return lock + "  - original: quay.io/example/other:v1\n    haulerRef: 127.0.0.1:5000/example/app:v1\n    sourceManifestDigest: " + r07SourceDigest + "\n    amd64ManifestDigest: " + r07PlatformDigest + "\n"
		},
		"haulerRef without tag": func(lock string) string {
			return strings.Replace(lock, "127.0.0.1:5000/example/app:v1", "127.0.0.1:5000/example/app", 1)
		},
	}
	base := "apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\nverificationToolImages:\n" +
		r07ImageEntry("quay.io/example/app:v1", "127.0.0.1:5000/example/app:v1", r07SourceDigest, r07PlatformDigest)
	for name, transform := range cases {
		_, err := ParseMaterialsLock([]byte(transform(base)))
		if err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}

	// An unknown digest that stays explicitly disabled is allowed: the lock
	// records the decision instead of fabricating a digest.
	disabled := base + "  - original: quay.io/example/thanos:v0\n    haulerRef: 127.0.0.1:5000/example/thanos:v0\n    sourceManifestDigest: null\n    amd64ManifestDigest: null\n    disabled: true\n    disabledReason: not shipped\n"
	if _, err := ParseMaterialsLock([]byte(disabled)); err != nil {
		t.Fatalf("a disabled entry with unknown digests must parse: %v", err)
	}
	// The same entry without the disabled marker must be rejected.
	enabled := strings.Replace(disabled, "    disabled: true\n    disabledReason: not shipped\n", "", 1)
	if _, err := ParseMaterialsLock([]byte(enabled)); err == nil {
		t.Fatal("an enabled entry with unknown digests must be rejected")
	}
}

// T-R07-04 (pure-function part): a locally injected docker-archive image has no
// source index digest, but its approval chain is the archive hash plus the
// conversion evidence. Missing evidence or archive hash must be rejected.
func TestMaterialsLockInjectionRules(t *testing.T) {
	entry := "  - original: docker.changqingyun.cn/kubercloud/kc-networking:dev\n" +
		"    haulerRef: 127.0.0.1:5000/kubercloud/kc-networking:dev\n" +
		"    amd64ManifestDigest: " + r07PlatformDigest + "\n" +
		"    injection:\n" +
		"      type: docker-archive\n" +
		"      archiveSha256: " + r07SourceDigest + "\n" +
		"      evidence: /tmp/kcn-tar-report.md\n" +
		"    use: kcn dev image (local injection)\n"
	lock, err := r07ParseSection(t, "verificationToolImages:\n"+entry)
	if err != nil {
		t.Fatalf("a complete injection entry must parse: %v", err)
	}
	if err := VerifyImageDigests(lock.Images[0], "", r07PlatformDigest); err != nil {
		t.Fatalf("an injected image verifies without a source digest: %v", err)
	}
	// The verification must refuse an observed source digest that the injection
	// never approved.
	if err := VerifyImageDigests(lock.Images[0], r07AnotherDigest, r07PlatformDigest); err == nil {
		t.Fatal("an observed source digest must fail for an injected image")
	}

	for name, broken := range map[string]string{
		"injection without evidence": strings.Replace(entry, "      evidence: /tmp/kcn-tar-report.md\n", "", 1),
		"injection without archive hash": strings.Replace(entry,
			"      archiveSha256: "+r07SourceDigest+"\n", "      archiveSha256: \"\"\n", 1),
		"injection with wrong type": strings.Replace(entry, "type: docker-archive", "type: tar-ball", 1),
	} {
		if _, err := r07ParseSection(t, broken); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	// An enabled image without any digest and without an injection record is the
	// "fabricated" shape and must be rejected.
	bare := strings.Replace(entry, "    injection:\n      type: docker-archive\n      archiveSha256: "+r07SourceDigest+"\n      evidence: /tmp/kcn-tar-report.md\n", "", 1)
	if _, err := r07ParseSection(t, bare); err == nil {
		t.Fatal("an enabled image with no digests and no injection record must be rejected")
	}
}

// T-R07-01 (pure-function part): a file material is verified against the hash
// approved in the lock. Recomputing an in-package SHA256SUMS cannot satisfy
// this check, because the expected value comes from the lock.
func TestMaterialsFileHashVerification(t *testing.T) {
	content := []byte("approved chart material")
	sum := sha256.Sum256(content)
	approved := hex.EncodeToString(sum[:])
	if err := VerifyFileMaterial("charts/example/1.0.0.tgz", content, approved); err != nil {
		t.Fatalf("the approved content must pass: %v", err)
	}
	if err := VerifyFileMaterial("charts/example/1.0.0.tgz", content, "sha256:"+approved); err != nil {
		t.Fatalf("the sha256: prefix must be accepted: %v", err)
	}
	tampered := append([]byte(nil), content...)
	tampered[len(tampered)-1] ^= 0xff
	err := VerifyFileMaterial("charts/example/1.0.0.tgz", tampered, approved)
	if err == nil {
		t.Fatal("tampered content must fail")
	}
	if !strings.Contains(err.Error(), "does not match the approved") {
		t.Fatalf("error = %v, want it to name both digests", err)
	}
	if err := VerifyFileMaterial("charts/example/1.0.0.tgz", content, "sha256:short"); err == nil {
		t.Fatal("a malformed approved hash must fail the check itself")
	}
}

// Chart and tool entries carry their own hash rules: malformed hashes and
// conflicting artifact paths must be rejected.
func TestMaterialsLockChartAndToolRules(t *testing.T) {
	section := `tools:
  helm:
    version: v3.20.0
    source: https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz
    sourceTarballSha256: dbb4c8fc8e19d159d1a63dda8db655f9ffa4aac1b9a6b188b34a40957119b286
    binarySha256: 1f7ed083dbc200a10fdfe04df94c21530140fdc955de5a1daed2694150f77b17
    artifactPath: bin/helm
components:
  cert-manager:
    chartVersion: v1.21.2
    chartSha256: 73a56e1728edd6c99f1f31082618c3259d279a76b7ebd3d4bdc5475c2442d34a
    artifactChartPath: charts/cert-manager/v1.21.2.tgz
`
	if _, err := r07ParseSection(t, section); err != nil {
		t.Fatalf("a well-formed chart and tool entry must parse: %v", err)
	}

	badHash := strings.Replace(section, "73a56e1728edd6c99f1f31082618c3259d279a76b7ebd3d4bdc5475c2442d34a",
		"73A56E1728EDD6C99F1F31082618C3259D279A76B7EBD3D4BDC5475C2442D34A", 1)
	if _, err := r07ParseSection(t, badHash); err == nil {
		t.Fatal("an uppercase chart hash must be rejected")
	}

	conflict := strings.Replace(section, "    artifactPath: bin/helm\n",
		"    artifactPath: charts/cert-manager/v1.21.2.tgz\n", 1)
	if _, err := r07ParseSection(t, conflict); err == nil {
		t.Fatal("a tool and a chart sharing one artifact path must be rejected")
	}

	missingPath := strings.Replace(section, "    artifactChartPath: charts/cert-manager/v1.21.2.tgz\n", "", 1)
	if _, err := r07ParseSection(t, missingPath); err == nil {
		t.Fatal("a chart without its artifact path must be rejected")
	}
}

// A batch section added somewhere new cannot escape validation: the walk is
// structural, not section-name based.
func TestMaterialsLockWalksNewSections(t *testing.T) {
	text := "apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\n" +
		"batch3:\n  futureImages:\n" +
		r07ImageEntry("quay.io/example/future:v1", "not-a-local-reference", r07SourceDigest, r07PlatformDigest)
	if _, err := ParseMaterialsLock([]byte(text)); err == nil {
		t.Fatal("an image entry inside a new section must still be validated")
	}
}

// R07.2: the packaged images.tsv and the materials lock must agree on every
// locked image. This runs against the real shipped files, so a lock entry and
// the table the installer renders from cannot drift apart unnoticed.
func TestMaterialsLockMatchesShippedImageTable(t *testing.T) {
	lock, err := LoadMaterialsLock(filepath.Join("..", "..", "ani", "components.lock.yaml"))
	if err != nil {
		t.Fatalf("parse lock: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "ani", "images.tsv"))
	if err != nil {
		t.Fatalf("read images.tsv: %v", err)
	}
	table, err := LoadImageTable(strings.Split(string(raw), "\n"))
	if err != nil {
		t.Fatalf("parse images.tsv: %v", err)
	}
	if err := lock.VerifyImageTableAgainstLock(table); err != nil {
		t.Fatalf("images.tsv does not satisfy the lock: %v", err)
	}
	// Negative control: a tsv row whose haulerRef was flipped must be caught.
	tampered := strings.Replace(string(raw), "127.0.0.1:5000/jetstack/cert-manager-controller:v1.21.2",
		"127.0.0.1:5000/jetstack/cert-manager-controller:v9.9.9", 1)
	table2, err := LoadImageTable(strings.Split(tampered, "\n"))
	if err != nil {
		t.Fatalf("parse tampered tsv: %v", err)
	}
	err = lock.VerifyImageTableAgainstLock(table2)
	if err == nil || !strings.Contains(err.Error(), "cert-manager-controller") {
		t.Fatalf("a flipped haulerRef must be caught against the lock, got: %v", err)
	}
}
