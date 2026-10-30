package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestSharedHandleUsage guards the per-workspace database split. In main.go a
// constructor that is handed the plain `database`/`dbx` handle reads and writes
// the SHARED database only, so a dedicated workspace's data would silently land
// in (or be missing from) the wrong place. Business data must use
// routedDB/routedDBX; only the control-plane pieces below may use the shared
// handle. Adding a repository forces a decision here.
func TestSharedHandleUsage(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	// Control-plane constructors that must stay on the shared database: they are
	// read before the workspace is known (login, API keys, routing itself) or
	// belong to the platform, not to one workspace.
	allowed := []string{
		"NewConnection", "NewDb(", "NewTenantRouter", "NewTenantRepository",
		"NewUserRepository", "NewSessionRepository", "NewPasswordResetRepository",
		"NewEmailVerificationRepository", "NewMFABackupCodeRepository", "NewMFALoginTokenRepository",
		"NewOAuthIdentityRepository", "NewSSOConnectionRepository", "NewSSOAssertionReplayRepository",
		"NewOrganizationRepository", "NewPlatformRepository",
		"NewCloudChargeRepository", "NewCloudBillingRepository", "NewCloudUsageRepository",
		"NewGatewayConnectionRepository", "NewWaitlistRepository",
		"NewFromEnv",                          // platform telemetry
		"SetPrimaryCountry", "sharedEntities", // signup stamps the shared primary entity (and the dedicated copy via the routed handle)                                                     // signup stamps the shared primary entity
		"routedDB := database",                                                  // the handle itself
		"database.Close", "database.PingContext", "database.Stats", "database:", // process plumbing
		"Fatalf", "log.Printf", "log.Println",
	}
	word := regexp.MustCompile(`\b(database|dbx)\b`)
	for i, line := range strings.Split(string(src), "\n") {
		code := line
		if j := strings.Index(code, "//"); j >= 0 {
			code = code[:j]
		}
		if !word.MatchString(code) {
			continue
		}
		ok := false
		for _, a := range allowed {
			if strings.Contains(code, a) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("main.go:%d uses the shared database handle for business data; use routedDB/routedDBX, or add the constructor to the allowed control-plane list with a reason:\n\t%s", i+1, strings.TrimSpace(line))
		}
	}
}
