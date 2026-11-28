package security

import "testing"

func TestOrganizationUnitRoutePermissions(t *testing.T) {
	for _, role := range []string{"admin", "operator", "analyst", "employee"} {
		if !AllowRequest(role, "GET", "/api/v1/organization/units") {
			t.Fatal("read denied", role)
		}
		for _, route := range []struct{ method, path string }{{"POST", "/api/v1/organization/units"}, {"PUT", "/api/v1/organization/units/unit-id"}} {
			if got := AllowRequest(role, route.method, route.path); got != (role == "admin") {
				t.Fatal("incorrect structure permission", role, route, got)
			}
		}
	}
}
