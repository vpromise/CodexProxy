package management

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func nodeTestTLS(t *testing.T) (string, string, []byte, *x509.Certificate) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "node-test"}, IsCA: true, BasicConstraintsValid: true, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	cert, e = x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	cp := filepath.Join(dir, "cert.pem")
	kp := filepath.Join(dir, "key.pem")
	raw, _ := x509.MarshalPKCS8PrivateKey(key)
	if e = os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), 0600); e != nil {
		t.Fatal(e)
	}
	return cp, kp, der, cert
}
func nodeTestServer(t *testing.T) (*Handler, *http.Server, *x509.Certificate) {
	t.Helper()
	h := intakeTestHandler(t)
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(h.cfg.AuthDir)
	h.authManager = coreauth.NewManager(store, nil, nil)
	cp, kp, der, cert := nodeTestTLS(t)
	sum := sha256.Sum256(der)
	h.cfg.ConsoleNode = config.ConsoleNodeConfig{Enabled: true, NodeID: "node-a", InstanceID: strings.Repeat("a", 32), Listen: "127.0.0.1:0", Cert: cp, Key: kp, ClientCA: cp, ConsoleCertificateSHA256: hex.EncodeToString(sum[:]), PublicOrigin: "https://console.example.test", EgressProfiles: []config.ConsoleEgressProfile{{ID: "egress", Version: "1", ProxyURL: "direct-ip://127.0.0.1"}}}
	h.cfg.AccessIsolation = config.AccessIsolation{Enabled: true, Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "claude"}}}
	h.configFilePath = filepath.Join(t.TempDir(), "config.yaml")
	if e := os.WriteFile(h.configFilePath, []byte("{}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); e != nil {
		t.Fatal(e)
	}
	h.selfServiceExchange = func(ctx context.Context, cfg *config.Config, r *selfServiceRecord, code string) (map[string]any, error) {
		m, e := intakeMockExchange(ctx, cfg, r, code)
		m["organization_uuid"] = "org-test"
		return m, e
	}
	srv, e := h.NewConsoleNodeServer(h.cfg)
	if e != nil {
		t.Fatal(e)
	}
	return h, srv, cert
}
func nodeCall(t *testing.T, srv *http.Server, cert *x509.Certificate, method, path string, body any, token string) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, "https://node.test"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://console.example.test")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)
	var result map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	return w.Code, result
}
func TestConsoleNodeEnrollmentLifecycle(t *testing.T) {
	h, srv, cert := nodeTestServer(t)
	id := strings.Repeat("b", 32)
	create := gin.H{"operation_id": id, "provider": "claude", "profile_id": "egress"}
	status, data := nodeCall(t, srv, cert, "POST", "/v1/enrollments", create, "")
	if status != 200 {
		t.Fatalf("create: %d %#v", status, data)
	}
	token := data["invitation_token"].(string)
	status, replay := nodeCall(t, srv, cert, "POST", "/v1/enrollments", create, "")
	if status != 200 || replay["invitation_token"] != nil {
		t.Fatalf("secret replay: %#v", replay)
	}
	changed := gin.H{"operation_id": id, "provider": "codex", "profile_id": "egress"}
	if code, _ := nodeCall(t, srv, cert, "POST", "/v1/enrollments", changed, ""); code != 409 {
		t.Fatal("operation payload change accepted")
	}
	if code, _ := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+id+"/start", gin.H{"consent": true}, "wrong"); code != 401 {
		t.Fatalf("missing invitation accepted: %d", code)
	}
	if code, data := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+id+"/start", gin.H{"consent": true}, token); code != 200 {
		t.Fatalf("start: %d %#v", code, data)
	}
	r, e := selfServiceRead(h.cfg.AuthDir+".self-service-oauth", id)
	if e != nil {
		t.Fatal(e)
	}
	var callback map[string]any
	_ = json.Unmarshal([]byte(intakeCompleteBody(r)), &callback)
	if code, data := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+id+"/complete", callback, token); code != 200 {
		t.Fatalf("complete: %d %#v", code, data)
	}
	if len(h.authManager.List()) != 0 {
		t.Fatal("pending registered")
	}
	if code, data := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+id+"/assign", gin.H{"operation_id": strings.Repeat("c", 32), "pool_id": "one"}, ""); code != 200 {
		t.Fatalf("assign: %d %#v", code, data)
	}
	_, data = nodeCall(t, srv, cert, "GET", "/v1/enrollments/"+id, nil, "")
	account, ok := data["account"].(map[string]any)
	if !ok || account["disabled"] != true || len(account["pools"].([]any)) != 1 {
		t.Fatalf("disabled assignment: %#v", data)
	}
	raw, _ := json.Marshal(data)
	for _, secret := range []string{"PRIVATE-ACCESS", "PRIVATE-REFRESH", r.Verifier, token, "proxy_url"} {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("credential leak")
		}
	}
	rev := account["revision"].(string)
	if code, data := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+id+"/enable", gin.H{"operation_id": strings.Repeat("d", 32), "expected_revision": rev}, ""); code != 200 {
		t.Fatalf("enable: %d %#v", code, data)
	}
	if code, _ := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+id+"/disable", gin.H{"operation_id": strings.Repeat("e", 32), "expected_revision": rev}, ""); code != 409 {
		t.Fatal("stale revision accepted")
	}
	a := h.authManager.List()[0]
	if _, e := h.authManager.PatchAuth(context.Background(), a.ID, a.RegistrationEpoch, func(latest *coreauth.Auth) error {
		applyAuthDisabledState(latest, true)
		latest.Metadata["expired"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	_, expired := nodeCall(t, srv, cert, "GET", "/v1/enrollments/"+id, nil, "")
	if expired["needs_reauth"] != true {
		t.Fatal("expired credential not surfaced")
	}
	ea := expired["account"].(map[string]any)
	if code, data := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+id+"/enable", gin.H{"operation_id": strings.Repeat("9", 32), "expected_revision": ea["revision"]}, ""); code != 409 || data["operation"].(map[string]any)["error"] != "needs_reauth_on_original_owner" {
		t.Fatal("expired account enabled")
	}
	// Reauthorization uses the original owner/profile and cannot enable or re-pool.
	renewal := strings.Repeat("7", 32)
	request := gin.H{"operation_id": renewal, "renewal_of": id, "expected_revision": ea["revision"], "provider": "claude", "profile_id": "egress"}
	code, renewData := nodeCall(t, srv, cert, "POST", "/v1/enrollments", request, "")
	if code != 200 {
		t.Fatalf("renewal invite %d %#v", code, renewData)
	}
	renewToken := renewData["invitation_token"].(string)
	if code, _ := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+renewal+"/start", gin.H{"consent": true}, renewToken); code != 200 {
		t.Fatal("renewal start")
	}
	renewRow, _ := selfServiceRead(h.cfg.AuthDir+".self-service-oauth", renewal)
	var renewCallback map[string]any
	json.Unmarshal([]byte(intakeCompleteBody(renewRow)), &renewCallback)
	if code, _ := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+renewal+"/complete", renewCallback, renewToken); code != 200 {
		t.Fatal("renewal complete")
	}
	before := h.authManager.List()[0]
	identityBefore := coreauth.AccountIdentity(before)
	if code, data := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+renewal+"/assign", gin.H{"operation_id": strings.Repeat("6", 32)}, ""); code != 200 {
		t.Fatalf("renewal approval %d %#v", code, data)
	}
	after := h.authManager.List()[0]
	if !after.Disabled || coreauth.AccountIdentity(after) != identityBefore || after.Metadata["self_service_id"] != id || after.Metadata["console_renewal_id"] != renewal || len(h.authManager.List()) != 1 {
		t.Fatal("renewal changed local identity or enabled/duplicated account")
	}
	_, renewObserved := nodeCall(t, srv, cert, "GET", "/v1/enrollments/"+renewal, nil, "")
	if renewObserved["needs_reauth"] != false || renewObserved["account"] == nil {
		t.Fatalf("renewal summary %#v", renewObserved)
	}
	// A later manual edit makes the previously captured revision unusable.
	request["operation_id"] = strings.Repeat("5", 32)
	if code, _ := nodeCall(t, srv, cert, "POST", "/v1/enrollments", request, ""); code != 409 {
		t.Fatal("stale renewal revision accepted")
	}
	// A different official account cannot replace this owner during renewal.
	_, rootState := nodeCall(t, srv, cert, "GET", "/v1/enrollments/"+id, nil, "")
	wrong := strings.Repeat("4", 32)
	request["operation_id"] = wrong
	request["expected_revision"] = rootState["account"].(map[string]any)["revision"]
	code, badInvite := nodeCall(t, srv, cert, "POST", "/v1/enrollments", request, "")
	if code != 200 {
		t.Fatal("second renewal")
	}
	badToken := badInvite["invitation_token"].(string)
	nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+wrong+"/start", gin.H{"consent": true}, badToken)
	badRow, _ := selfServiceRead(h.cfg.AuthDir+".self-service-oauth", wrong)
	var badCallback map[string]any
	json.Unmarshal([]byte(intakeCompleteBody(badRow)), &badCallback)
	h.selfServiceExchange = func(ctx context.Context, cfg *config.Config, r *selfServiceRecord, code string) (map[string]any, error) {
		m, e := intakeMockExchange(ctx, cfg, r, code)
		m["organization_uuid"] = "org-test"
		m["account_uuid"] = "another-person"
		return m, e
	}
	nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+wrong+"/complete", badCallback, badToken)
	if code, _ := nodeCall(t, srv, cert, "POST", "/v1/enrollments/"+wrong+"/assign", gin.H{"operation_id": strings.Repeat("3", 32)}, ""); code != 409 {
		t.Fatal("different principal replaced owner")
	}
	if coreauth.AccountIdentity(h.authManager.List()[0]) != identityBefore {
		t.Fatal("owner changed after rejected renewal")
	}
	// A credential file that has not been reloaded is not a deletion tombstone.
	accepted, _ := selfServiceRead(h.cfg.AuthDir+".self-service-oauth", id)
	if _, e = os.Stat(filepath.Join(h.cfg.AuthDir, accepted.FileName)); e != nil {
		t.Fatal(e)
	}
}
func TestConsoleNodeMTLSAndReconfiguration(t *testing.T) {
	h, srv, cert := nodeTestServer(t)
	req := httptest.NewRequest("GET", "https://node/v1/inventory", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("missing mTLS accepted")
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- ServeConsoleNode(srv, listener) }()
	defer func() { srv.Close(); <-done }()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: srv.TLSConfig.Certificates, MinVersion: tls.VersionTLS13}}}
	resp, e := client.Get("https://" + listener.Addr().String() + "/v1/capabilities")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	h.mu.Lock()
	h.cfg = h.cfg.CloneForRuntime()
	h.cfg.ConsoleNode.EgressProfiles[0].Version = "2"
	h.mu.Unlock()
	if code, _ := nodeCall(t, srv, cert, "GET", "/v1/inventory", nil, ""); code != 503 {
		t.Fatal("hot changed route accepted")
	}
}
func TestConsoleScopedKeyIdempotenceAndDeletion(t *testing.T) {
	h, srv, cert := nodeTestServer(t)
	_, inventory := nodeCall(t, srv, cert, "GET", "/v1/inventory", nil, "")
	id := strings.Repeat("f", 32)
	request := gin.H{"operation_id": id, "pool_id": "one", "expected_revision": inventory["policy_revision"]}
	code, data := nodeCall(t, srv, cert, "POST", "/v1/channel-keys", request, "")
	if code != 200 {
		t.Fatalf("key %d %#v", code, data)
	}
	key := data["key"]
	code, replay := nodeCall(t, srv, cert, "POST", "/v1/channel-keys", request, "")
	if code != 200 || replay["key"] != key {
		t.Fatal("key replay failed")
	}
	h.mu.Lock()
	cfg := h.cfg.CloneForRuntime()
	cfg.APIKeys = nil
	cfg.AccessIsolation.Keys = nil
	config.SaveConfigPreserveComments(h.configFilePath, cfg)
	h.cfg = cfg
	h.mu.Unlock()
	code, _ = nodeCall(t, srv, cert, "POST", "/v1/channel-keys", request, "")
	if code != 409 {
		t.Fatal("deleted key resurrected")
	}
}

func TestConsoleUncertainDeletedKeyDoesNotRecreate(t *testing.T) {
	h, srv, cert := nodeTestServer(t)
	_, inventory := nodeCall(t, srv, cert, "GET", "/v1/inventory", nil, "")
	id := strings.Repeat("8", 32)
	request := gin.H{"operation_id": id, "pool_id": "one", "expected_revision": inventory["policy_revision"]}
	if code, _ := nodeCall(t, srv, cert, "POST", "/v1/channel-keys", request, ""); code != 200 {
		t.Fatal("create key")
	}
	path := filepath.Join(h.cfg.AuthDir+".console-node", id+".json")
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var op consoleOperation
	json.Unmarshal(raw, &op)
	op.Status = "uncertain"
	if e = atomicConsoleWrite(path, op); e != nil {
		t.Fatal(e)
	}
	h.mu.Lock()
	cfg := h.cfg.CloneForRuntime()
	cfg.APIKeys = nil
	cfg.AccessIsolation.Keys = nil
	config.SaveConfigPreserveComments(h.configFilePath, cfg)
	h.cfg = cfg
	h.mu.Unlock()
	if code, _ := nodeCall(t, srv, cert, "POST", "/v1/channel-keys", request, ""); code != 409 {
		t.Fatal("uncertain key resurrected after deletion")
	}
}

func TestConsoleNodeCapabilityContract(t *testing.T) {
	_, srv, cert := nodeTestServer(t)
	code, result := nodeCall(t, srv, cert, "GET", "/v1/capabilities", nil, "")
	if code != 200 {
		t.Fatal(code)
	}
	raw, e := os.ReadFile("testdata/console-node-v1-capabilities.json")
	if e != nil {
		t.Fatal(e)
	}
	var expected map[string]any
	if e = json.Unmarshal(raw, &expected); e != nil {
		t.Fatal(e)
	}
	if digestJSON(result) != digestJSON(expected) {
		t.Fatalf("v1 capability contract changed: %#v", result)
	}
}
