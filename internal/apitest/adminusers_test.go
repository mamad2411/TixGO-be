package apitest

import "testing"

func TestAdminBlockUserRevokesSessionAndBlocksLogin(t *testing.T) {
	e := NewEnv(t)
	admin := e.UserWithRole("admin", "Admin Satu")
	u := e.Verified("Penipu Kecil")
	var sr []map[string]any
	admin.GET(api + "/admin/users?q=" + u.Email[:6]).Expect(200).Data(&sr)
	if len(sr) == 0 {
		t.Fatal("pencarian pengguna")
	}
	admin.POST(api+"/admin/users/"+u.UserID+"/block", M{"reason": ""}).ExpectErr(400, "VALIDATION_FAILED")
	admin.POST(api+"/admin/users/"+admin.UserID+"/block", M{"reason": "uji coba blokir diri"}).ExpectErr(403, "FORBIDDEN")
	admin.POST(api+"/admin/users/"+u.UserID+"/block", M{"reason": "dugaan percaloan"}).Expect(204)
	u.GET(api + "/me/privacy/requests").Expect(401) // sesi dicabut
	e.Anon().POST(api+"/auth/login", M{"email": u.Email, "password": testPassword}).Expect(403)
	admin.POST(api+"/admin/users/"+u.UserID+"/unblock", nil).Expect(204)
	e.Anon().POST(api+"/auth/login", M{"email": u.Email, "password": testPassword}).Expect(200)
	if e.QueryInt("SELECT count(*) FROM audit_logs WHERE action IN ('user.block','user.unblock')") != 2 {
		t.Fatal("teraudit")
	}
}

func TestAdminRoleChangeRulesAndBlacklist(t *testing.T) {
	e := NewEnv(t)
	admin := e.UserWithRole("admin", "Admin Dua")
	u := e.Verified("Calon Support")
	other := e.UserWithRole("admin", "Admin Lain")
	admin.PUT(api+"/admin/users/"+u.UserID+"/role", M{"role": "admin"}).ExpectErr(400, "VALIDATION_FAILED")
	admin.PUT(api+"/admin/users/"+other.UserID+"/role", M{"role": "support"}).ExpectErr(403, "FORBIDDEN")
	admin.PUT(api+"/admin/users/"+u.UserID+"/role", M{"role": "support"}).Expect(204)
	if e.QueryStr("SELECT role FROM users WHERE id=$1", u.UserID) != "support" {
		t.Fatal("role berubah")
	}
	// blacklist: validasi + efek (akses ditolak) + tidak menyimpan nilai mentah
	admin.POST(api+"/admin/blacklist", M{"kind": "ip", "value": "bukan-ip", "reason": "uji validasi"}).ExpectErr(400, "VALIDATION_FAILED")
	admin.POST(api+"/admin/blacklist", M{"kind": "user", "value": admin.UserID, "reason": "uji blokir admin"}).ExpectErr(403, "FORBIDDEN")
	victim := e.Verified("Korban Blacklist")
	var bl struct {
		ID        string
		ValueHash string `json:"value_hash"`
	}
	admin.POST(api+"/admin/blacklist", M{"kind": "user", "value": victim.UserID, "reason": "multi akun massal"}).Expect(201).Data(&bl)
	if bl.ValueHash == victim.UserID {
		t.Fatal("nilai mentah tidak boleh disimpan")
	}
	e.Anon().POST(api+"/auth/login", M{"email": victim.Email, "password": testPassword}).Expect(403)
	admin.DELETE(api + "/admin/blacklist/" + bl.ID).Expect(204)
	e.Anon().POST(api+"/auth/login", M{"email": victim.Email, "password": testPassword}).Expect(200)
	var sig []map[string]any
	admin.GET(api + "/admin/users/" + victim.UserID + "/risk-signals").Expect(200).Data(&sig)
	// pengguna biasa dilarang
	e.Verified("Pengguna Biasa").GET(api+"/admin/blacklist").ExpectErr(403, "FORBIDDEN")
}
