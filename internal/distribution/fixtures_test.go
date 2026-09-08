package distribution

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallstep/pkcs7"
	"howett.net/plist"
)

type cryptoFixture struct {
	now                              time.Time
	root, profileSigner, signingCert *x509.Certificate
	rootKey, profileKey, signingKey  *rsa.PrivateKey
}

func newCryptoFixture(t *testing.T) *cryptoFixture {
	t.Helper()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	rootKey := mustRSA(t)
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fixture Root"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	makeLeaf := func(serial int64, cn string) (*x509.Certificate, *rsa.PrivateKey) {
		key := mustRSA(t)
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(30 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}}
		der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return cert, key
	}
	profileSigner, profileKey := makeLeaf(2, "Fixture Profile Signer")
	signingCert, signingKey := makeLeaf(3, "Fixture Distribution")
	return &cryptoFixture{now: now, root: root, profileSigner: profileSigner, signingCert: signingCert, rootKey: rootKey, profileKey: profileKey, signingKey: signingKey}
}

func mustRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

type profileOptions struct {
	uuid, bundle, team, prefix string
	creation, expiry           time.Time
	devicesPresent             bool
	devices                    []string
	enterprise, getTaskAllow   bool
	binary                     bool
	developerCert              *x509.Certificate
	signer, parent             *x509.Certificate
	signerKey                  *rsa.PrivateKey
	extraEntitlements          map[string]any
}

func (f *cryptoFixture) profile(t *testing.T, options profileOptions) []byte {
	t.Helper()
	if options.bundle == "" {
		options.bundle = "com.example.Pomeforge"
	}
	if options.team == "" {
		options.team = "TEAM123456"
	}
	if options.uuid == "" {
		options.uuid = "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"
	}
	if options.prefix == "" {
		options.prefix = options.team
	}
	if options.creation.IsZero() {
		options.creation = f.now.Add(-time.Hour)
	}
	if options.expiry.IsZero() {
		options.expiry = f.now.Add(24 * time.Hour)
	}
	if options.developerCert == nil {
		options.developerCert = f.signingCert
	}
	if options.signer == nil {
		options.signer = f.profileSigner
	}
	if options.parent == nil {
		options.parent = f.root
	}
	if options.signerKey == nil {
		options.signerKey = f.profileKey
	}
	entitlements := map[string]any{
		"application-identifier":              options.prefix + "." + options.bundle,
		"com.apple.developer.team-identifier": options.team,
		"get-task-allow":                      options.getTaskAllow,
		"keychain-access-groups":              []string{options.prefix + ".*"},
	}
	for k, v := range options.extraEntitlements {
		entitlements[k] = v
	}
	profile := map[string]any{
		"UUID": options.uuid, "Name": "Fixture App Store",
		"CreationDate": options.creation, "ExpirationDate": options.expiry,
		"TeamIdentifier": []string{options.team}, "ApplicationIdentifierPrefix": []string{options.prefix},
		"DeveloperCertificates": [][]byte{options.developerCert.Raw}, "Entitlements": entitlements,
	}
	if options.devicesPresent {
		profile["ProvisionedDevices"] = options.devices
	}
	if options.enterprise {
		profile["ProvisionsAllDevices"] = true
	}
	format := plist.XMLFormat
	if options.binary {
		format = plist.BinaryFormat
	}
	content, err := plist.Marshal(profile, format)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := pkcs7.NewSignedData(content)
	if err != nil {
		t.Fatal(err)
	}
	if err := signed.AddSignerChain(options.signer, options.signerKey, []*x509.Certificate{options.parent}, pkcs7.SignerInfoConfig{}); err != nil {
		t.Fatal(err)
	}
	cms, err := signed.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return cms
}

type identityPaths struct{ config, key, cert, profile, root string }

func (f *cryptoFixture) writeIdentity(t *testing.T, dir string, profile []byte, key *rsa.PrivateKey, cert *x509.Certificate, root *x509.Certificate) identityPaths {
	t.Helper()
	if key == nil {
		key = f.signingKey
	}
	if cert == nil {
		cert = f.signingCert
	}
	if root == nil {
		root = f.root
	}
	p := identityPaths{config: filepath.Join(dir, "identity.json"), key: filepath.Join(dir, "signing.key"), cert: filepath.Join(dir, "signing.pem"), profile: filepath.Join(dir, "profile.mobileprovision"), root: filepath.Join(dir, "root.pem")}
	writePrivate(t, p.key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	writePrivate(t, p.cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
	writePrivate(t, p.profile, profile)
	writePrivate(t, p.root, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}))
	c := SigningConfig{Version: 1, PrivateKeyPath: p.key, CertificatePath: p.cert, ProvisioningProfilePath: p.profile, TrustedRootPaths: []string{p.root}, Metadata: map[string]string{"label": "fixture"}}
	if err := CreateSigningConfig(context.Background(), p.config, c); err != nil {
		t.Fatal(err)
	}
	return p
}

func writePrivate(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureMachO() []byte {
	b := new(bytes.Buffer)
	for _, v := range []uint32{0xfeedfacf, 0x0100000c, 0, 2, 1, 24, 0, 0, lcBuildVersion, 24, platformIOS, 17 << 16, 26 << 16, 0} {
		_ = binary.Write(b, binary.LittleEndian, v)
	}
	return b.Bytes()
}

func fixtureInfo(t *testing.T, binaryFormat bool) []byte {
	t.Helper()
	info := map[string]any{
		"CFBundleIdentifier": "com.example.Pomeforge", "CFBundleShortVersionString": "1.2.3", "CFBundleVersion": "42", "CFBundleExecutable": "Pomeforge", "MinimumOSVersion": "17.0",
		"UIDeviceFamily": []int{1, 2}, "CFBundleIconName": "AppIcon",
		"CFBundleIcons":      map[string]any{"CFBundlePrimaryIcon": map[string]any{"CFBundleIconName": "AppIcon", "CFBundleIconFiles": []string{"AppIcon20x20", "AppIcon29x29", "AppIcon40x40", "AppIcon60x60"}}},
		"CFBundleIcons~ipad": map[string]any{"CFBundlePrimaryIcon": map[string]any{"CFBundleIconName": "AppIcon", "CFBundleIconFiles": []string{"AppIcon20x20", "AppIcon29x29", "AppIcon40x40", "AppIcon76x76", "AppIcon83.5x83.5"}}},
		"DTPlatformName":     "iphoneos", "DTPlatformVersion": "26.0", "DTPlatformBuild": "23A1", "DTSDKName": "iphoneos26.0", "DTSDKBuild": "23A1", "DTXcode": "2600", "DTXcodeBuild": "17A1",
	}
	format := plist.XMLFormat
	if binaryFormat {
		format = plist.BinaryFormat
	}
	b, err := plist.Marshal(info, format)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtureInfoWith(t *testing.T, key string, value any) []byte {
	t.Helper()
	var info map[string]any
	if _, err := plist.Unmarshal(fixtureInfo(t, false), &info); err != nil {
		t.Fatal(err)
	}
	info[key] = value
	b, err := plist.Marshal(info, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeBundle(t *testing.T, dir string, binaryInfo bool) string {
	t.Helper()
	app := filepath.Join(dir, "Pomeforge.app")
	if err := os.Mkdir(app, 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivate(t, filepath.Join(app, "Info.plist"), fixtureInfo(t, binaryInfo))
	if err := os.WriteFile(filepath.Join(app, "Pomeforge"), fixtureMachO(), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCompiledIcons(t, app, "AppIcon")
	return app
}

type zipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func writeIPA(t *testing.T, file string, entries []zipEntry) {
	t.Helper()
	out, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			h.SetMode(entry.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func validIPAEntries(t *testing.T, profile []byte, binaryInfo bool) []zipEntry {
	t.Helper()
	entries := []zipEntry{
		{"Payload/Pomeforge.app/Info.plist", fixtureInfo(t, binaryInfo), 0o600},
		{"Payload/Pomeforge.app/Pomeforge", fixtureMachO(), 0o700},
		{"Payload/Pomeforge.app/embedded.mobileprovision", profile, 0o600},
	}
	for _, requirement := range compiledIconRequirements("AppIcon") {
		entries = append(entries, zipEntry{"Payload/Pomeforge.app/" + requirement.filename, fixturePNG(t, requirement.dimension), 0o600})
	}
	return entries
}

func fixturePNG(t *testing.T, dimension int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, dimension, dimension))
	for y := 0; y < dimension; y++ {
		for x := 0; x < dimension; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 100, B: 180, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func writeCompiledIcons(t *testing.T, app, primary string) {
	t.Helper()
	for _, requirement := range compiledIconRequirements(primary) {
		writePrivate(t, filepath.Join(app, requirement.filename), fixturePNG(t, requirement.dimension))
	}
}

func zipBundle(t *testing.T, app, output, profile string, extra ...zipEntry) {
	t.Helper()
	var entries []zipEntry
	err := filepath.Walk(app, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(filepath.Dir(app), name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		// Exact zsign 1.1.2 emits DOS-style attributes that Go reports as 0666,
		// including for the executable. Export must canonicalize these modes.
		entries = append(entries, zipEntry{name: filepath.ToSlash(filepath.Join("Payload", rel)), data: data, mode: 0o666})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	profileData, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, zipEntry{name: "Payload/Pomeforge.app/embedded.mobileprovision", data: profileData, mode: 0o600})
	entries = append(entries, zipEntry{name: "Payload/Pomeforge.app/_CodeSignature/CodeResources", data: []byte("synthetic-not-a-real-signature"), mode: 0o600})
	entries = append(entries, extra...)
	writeIPA(t, output, entries)
}

func writeIconCatalog(t *testing.T, parent string) string {
	t.Helper()
	catalog := filepath.Join(parent, "Assets.xcassets")
	set := filepath.Join(catalog, "AppIcon.appiconset")
	if err := os.MkdirAll(set, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := catalogContents{Info: map[string]any{"author": "fixture", "version": 1}}
	for i, slot := range requiredIconSlots {
		dim, err := parseDimension(slot.size, slot.scale)
		if err != nil {
			t.Fatal(err)
		}
		filename := fmt.Sprintf("icon-%02d.png", i)
		img := image.NewRGBA(image.Rect(0, 0, dim, dim))
		for y := 0; y < dim; y++ {
			for x := 0; x < dim; x++ {
				img.Set(x, y, color.RGBA{R: 20, G: 100, B: 180, A: 255})
			}
		}
		f, err := os.OpenFile(filepath.Join(set, filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		contents.Images = append(contents.Images, catalogImage{Idiom: slot.idiom, Size: slot.size, Scale: slot.scale, Filename: filename})
	}
	b, err := json.Marshal(contents)
	if err != nil {
		t.Fatal(err)
	}
	writePrivate(t, filepath.Join(set, "Contents.json"), b)
	return catalog
}

func treeDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.Walk(root, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, name)
		_, _ = io.WriteString(h, rel+info.Mode().String())
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			_, _ = h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
