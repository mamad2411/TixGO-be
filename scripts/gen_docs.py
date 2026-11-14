#!/usr/bin/env python3
"""Membangkitkan dokumen rujukan dari sumber kebenaran (kode & migrasi) agar tidak melenceng.

  docs/references/API-ROUTES.md          ← api/openapi.yaml (yang dibangkitkan dari router & dijaga tes)
  docs/references/ENV-GENERATED.md       ← internal/config/config.go
  docs/references/DB-SCHEMA-GENERATED.md ← migrasi nyata (butuh Postgres; DB sementara dibuat & dihapus)
  docs/references/TEST-MATRIX.md         ← daftar tes integrasi/unit (`go test -list`)

Pakai: python3 scripts/gen_docs.py   (dari folder backend/; butuh psql, migrate, go, pyyaml)
"""
import os, re, subprocess, sys, yaml, textwrap

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
REF = os.path.join(ROOT, "..", "docs", "references")
os.makedirs(REF, exist_ok=True)
HEAD = "<!-- DIBANGKITKAN oleh backend/scripts/gen_docs.py — jangan edit tangan -->\n"


def api_routes():
    spec = yaml.safe_load(open(os.path.join(ROOT, "api", "openapi.yaml")))
    by_tag = {}
    for path, item in spec["paths"].items():
        for method, op in item.items():
            if method not in ("get", "post", "put", "patch", "delete"):
                continue
            sec = op.get("security")
            auth = "publik" if sec == [] else ("API key" if sec and "apiKey" in sec[0] else "login")
            by_tag.setdefault(op["tags"][0], []).append((path, method.upper(), op.get("summary", ""), auth))
    out = [HEAD, "# Daftar Route API\n", f"Total **{sum(len(v) for v in by_tag.values())}** operasi. Skema lengkap: `backend/api/openapi.yaml` (dibangkitkan dari router, dijaga tes `TestOpenAPIMatchesRouter`).\n"]
    for tag in sorted(by_tag):
        out.append(f"\n## {tag}\n\n| Method | Path | Ringkasan | Akses |\n|---|---|---|---|")
        for path, m, s, a in sorted(by_tag[tag]):
            out.append(f"| {m} | `{path}` | {s} | {a} |")
    open(os.path.join(REF, "API-ROUTES.md"), "w").write("\n".join(out) + "\n")


def env_doc():
    src = open(os.path.join(ROOT, "internal", "config", "config.go")).read()
    rows = {}
    for kind, key, rest in re.findall(r'\b(req|opt|num|oneOf|dur|getenv|splitCSV\(opt|splitCSV\(req)\(?"([A-Z][A-Z0-9_]+)"([^)\n]*)', src):
        rows.setdefault(key, (kind, rest.strip(", ")))
    for key in re.findall(r'getenv\("([A-Z][A-Z0-9_]+)"\)', src):
        rows.setdefault(key, ("getenv", ""))
    out = [HEAD, "# Registri Env (dari config.go)\n", "`req` = wajib (gagal start bila kosong); lainnya opsional dengan default. Daftar manusiawi & catatan: `docs/13 §2`.\n", "\n| Env | Jenis | Default / opsi |\n|---|---|---|"]
    for key in sorted(rows):
        kind, rest = rows[key]
        shown = rest.replace('|', '/')
        if kind == 'oneOf':
            parts = [x.strip().strip('"') for x in rest.split(',') if x.strip()]
            if parts:
                shown = f"default `{parts[0]}`; pilihan: " + ", ".join(f"`{x}`" for x in parts[1:])
        out.append(f"| `{key}` | {'wajib' if kind in ('req','splitCSV(req') else 'opsional'} | {shown or '—'} |")
    open(os.path.join(REF, "ENV-GENERATED.md"), "w").write("\n".join(out) + "\n")


def psql(db, sql):
    env = dict(os.environ, PGPASSWORD=os.environ.get("PGPASSWORD", "tixgo"))
    host, port, user = os.environ.get("PGHOST", "localhost"), os.environ.get("PGPORT", "5433"), os.environ.get("PGUSER", "tixgo")
    return subprocess.run(["psql", "-h", host, "-p", port, "-U", user, "-d", db, "-AtF", "\t", "-c", sql], capture_output=True, text=True, env=env, check=True).stdout


def schema_doc():
    db = "tixgo_docgen"
    psql("postgres", f"DROP DATABASE IF EXISTS {db}")
    psql("postgres", f"CREATE DATABASE {db}")
    try:
        dsn = f"postgres://{os.environ.get('PGUSER','tixgo')}:{os.environ.get('PGPASSWORD','tixgo')}@{os.environ.get('PGHOST','localhost')}:{os.environ.get('PGPORT','5433')}/{db}?sslmode=disable"
        subprocess.run(["migrate", "-path", os.path.join(ROOT, "migrations"), "-database", dsn, "up"], check=True, capture_output=True)
        tables = [t for t in psql(db, "select tablename from pg_tables where schemaname='public' and tablename<>'schema_migrations' order by 1").split("\n") if t]
        out = [HEAD, "# Skema DB (dari migrasi nyata)\n", f"**{len(tables)} tabel.** Keterangan konvensi & invarian: `docs/09-DB-SCHEMA.md`.\n"]
        for t in tables:
            out.append(f"\n## `{t}`\n\n| Kolom | Tipe | Null | Default |\n|---|---|---|---|")
            cols = psql(db, f"select column_name, format_type(a.atttypid,a.atttypmod), case when a.attnotnull then 'NOT NULL' else '' end, coalesce(pg_get_expr(d.adbin,d.adrelid),'') from pg_attribute a join pg_class c on c.oid=a.attrelid join pg_namespace n on n.oid=c.relnamespace left join pg_attrdef d on d.adrelid=a.attrelid and d.adnum=a.attnum join information_schema.columns ic on ic.table_name=c.relname and ic.column_name=a.attname and ic.table_schema='public' where c.relname='{t}' and n.nspname='public' and a.attnum>0 and not a.attisdropped order by ic.ordinal_position")
            for line in cols.rstrip("\n").split("\n"):
                c = (line.split("\t") + ["", "", "", ""])[:4]
                out.append(f"| `{c[0]}` | {c[1]} | {c[2]} | {c[3].replace('|','/')} |")
            cons = psql(db, f"select conname, pg_get_constraintdef(oid) from pg_constraint where conrelid='public.{t}'::regclass order by contype, conname")
            cl = [(l.split("\t") + ["", ""])[:2] for l in cons.rstrip("\n").split("\n") if l]
            if cl:
                out.append("\nConstraint: " + "; ".join(f"`{n}` {d.replace('|','/')}" for n, d in cl))
            idx = psql(db, f"select indexname, indexdef from pg_indexes where schemaname='public' and tablename='{t}' and indexname not in (select conname from pg_constraint where conrelid='public.{t}'::regclass) order by 1")
            il = [(l.split("\t") + ["", ""])[:2] for l in idx.rstrip("\n").split("\n") if l]
            if il:
                out.append("\n\nIndeks: " + "; ".join(f"`{n}`" for n, _ in il))
        open(os.path.join(REF, "DB-SCHEMA-GENERATED.md"), "w").write("\n".join(out) + "\n")
    finally:
        psql("postgres", f"DROP DATABASE IF EXISTS {db}")


def test_matrix():
    res = subprocess.run(["go", "test", "-list", ".", "./..."], cwd=ROOT, capture_output=True, text=True)
    pkg_tests, cur = {}, None
    for line in res.stdout.splitlines():
        if line.startswith("ok") or line.startswith("?"):
            parts = line.split()
            pkg = parts[1].replace("tixgo/backend/", "") if len(parts) > 1 else ""
            pkg_tests[pkg] = pkg_tests.pop("__cur", [])
        elif line.startswith("Test"):
            pkg_tests.setdefault("__cur", []).append(line.strip())
    pkg_tests.pop("__cur", None)
    total = sum(len(v) for v in pkg_tests.values())
    out = [HEAD, "# Matriks Tes\n", f"**{total} tes** (`go test ./...`). Integrasi: `internal/apitest` (DB `tixgo_test`, Redis DB 15, jam/gateway/mailer palsu). Detail T-ID regresi: `docs/13 §5`.\n"]
    for pkg in sorted(pkg_tests):
        if pkg_tests[pkg]:
            out.append(f"\n## `{pkg}` ({len(pkg_tests[pkg])})\n")
            out.extend(f"- `{t}`" for t in pkg_tests[pkg])
    open(os.path.join(REF, "TEST-MATRIX.md"), "w").write("\n".join(out) + "\n")


def catalog_doc():
    """Katalog: kode error+status, loop worker, topik outbox/WS, akses role, enum status, knob waktu."""
    import glob, collections
    svc = glob.glob(os.path.join(ROOT, "internal", "**", "*.go"), recursive=True)
    svc = [f for f in svc if not f.endswith("_test.go")]
    src = {f: open(f).read() for f in svc}
    allsrc = "\n".join(src.values())
    out = [HEAD, "# Katalog Backend (dibangkitkan dari kode)\n"]

    # 1. kode error
    consts = dict(re.findall(r'(Code\w+)\s*=\s*"([A-Z_]+)"', src[os.path.join(ROOT, "internal", "apperr", "apperr.go")]))
    ctor = {"BadRequest": 400, "Validation": 400, "Unauthorized": 401, "Forbidden": 403, "ForbiddenCode": 403,
            "NotFound": 404, "Conflict": 409, "Unprocessable": 422, "TooMany": 429}
    smap = {"StatusBadRequest": 400, "StatusUnauthorized": 401, "StatusForbidden": 403, "StatusNotFound": 404,
            "StatusConflict": 409, "StatusUnprocessableEntity": 422, "StatusTooManyRequests": 429,
            "StatusServiceUnavailable": 503, "StatusInternalServerError": 500, "StatusPaymentRequired": 402,
            "StatusGone": 410, "StatusLocked": 423, "StatusRequestEntityTooLarge": 413}
    seen = collections.defaultdict(collections.Counter)
    for m in re.finditer(r'apperr\.(\w+)\(\s*apperr\.(Code\w+)', allsrc):
        if m.group(1) in ctor: seen[m.group(2)][ctor[m.group(1)]] += 1
    for m in re.finditer(r'apperr\.New\(\s*http\.(Status\w+)\s*,\s*apperr\.(Code\w+)', allsrc):
        seen[m.group(2)][smap.get(m.group(1), 0)] += 1
    for m in re.finditer(r'apperr\.New\(\s*(\d{3})\s*,\s*apperr\.(Code\w+)', allsrc):
        seen[m.group(2)][int(m.group(1))] += 1

    for m in re.finditer(r'(http\.Status\w+)[^\n]*"code":\s*"([A-Z_]+)"', allsrc):
        seen["Code" + "".join(w.title() for w in m.group(2).split("_"))][smap.get(m.group(1).split(".")[1], 0)] += 1
        seen["__lit__" + m.group(2)][1] += 1
    seen["CodeValidation"][400] += 1; seen["CodeNotFound"][404] += 1; seen["CodeUnauthenticated"][401] += 1
    seen["CodeForbidden"][403] += 1; seen["CodeRateLimited"][429] += 1
    out.append(f"## 1. Kode error ({len(consts)} konstanta + `INTERNAL` 500)\n\nStatus = status yang dipakai di titik pembuatan error (bisa >1 bila satu kode dipakai di beberapa konteks). `0 pemakaian` = konstanta tanpa call site langsung (periksa: mati, atau dibuat lewat helper lain).\n\n| Kode | Status HTTP | Pemakaian |\n|---|---|---|")
    for c, v in sorted(consts.items(), key=lambda kv: kv[1]):
        cnt = seen.get(c, {}) or seen.get("__lit__" + v) and {503: 1} or {}
        st = ", ".join(str(k) for k in sorted(cnt)) or "?"
        out.append(f"| `{v}` | {st} | {sum(cnt.values()) or len(re.findall(r'apperr\\.'+c+r'\\b', allsrc))} |")

    # 2. loop
    sched = src[os.path.join(ROOT, "internal", "service", "sched.go")]
    unit = {"time.Millisecond": "ms", "time.Second": "dtk", "time.Minute": "mnt", "time.Hour": "jam"}
    loops = []
    for name, mult, fn in re.findall(r'\{"(\w+)",\s*([^,]+),\s*(s\.[\w.]+)\}', sched):
        m = re.match(r'(?:(\d+)\s*\*\s*)?(time\.\w+)', mult.strip())
        n = m.group(1) or "1"; loops.append((name, f"{n} {unit[m.group(2)]}", fn))
    out.append(f"\n## 2. Loop periodik worker ({len(loops)}; kunci Redis per loop, aman >1 replika)\n\n| Loop | Interval | Fungsi |\n|---|---|---|")
    out += [f"| `{a}` | {b} | `{c}` |" for a, b, c in loops]
    jc = dict(re.findall(r'(job\w+)\s*=\s*"([\w.:-]+)"', allsrc))
    jobs = [jc.get(n, n) for n in re.findall(r'Reg\.Handle\((\w+)', allsrc)]
    out.append(f"\n**Job asynq terdaftar** ({len(jobs)}): " + (", ".join(f"`{j}`" for j in sorted(set(jobs))) or "—"))

    # 3. topik realtime
    topics = set()
    for chan, topic in re.findall(r'publish\(ctx,\s*([^,]+),\s*"([\w.]+)"', allsrc): topics.add((chan.strip(), topic))
    for chan, topic in re.findall(r'emit\(ctx,\s*\w+,\s*([^,]+),\s*"([\w.]+)"', allsrc): topics.add((chan.strip(), topic))
    for chan, topic in re.findall(r'Channel:\s*([^,]+),\s*Topic:\s*"([\w.]+)"', allsrc): topics.add((chan.strip(), topic))
    out.append("\n## 3. Kejadian realtime (outbox → Redis → WS)\n\nKejadian wajib-pasti ditulis ke tabel `outbox` dalam transaksi; relay tiap 500 ms. Klien `sub` per channel; otorisasi di `ws.Authorizer`.\n\n| Channel (ekspresi) | Topic |\n|---|---|")
    out += [f"| `{c}` | `{t}` |" for c, t in sorted(topics, key=lambda x: x[1])]

    # 4. akses
    out.append("\n## 4. Akses per grup route\n\nRole valid (CHECK di `users.role`): " + ", ".join(f"`{r}`" for r in re.findall(r"'(\w+)'", re.search(r"role IN \(([^)]*)\)", open(os.path.join(ROOT, "migrations", "0001_identity.up.sql")).read()).group(1))) + ". Role tim organizer: `owner`, `member`, `gate_staff`. MFA wajib global untuk: admin, finance (`MFARequiredRole`).\n\n| Grup route | Role | MFA di grup |\n|---|---|---|")
    rows = set()
    for f, t in src.items():
        if "/handler/" not in f: continue
        for m in re.finditer(r'Group\("([^"]*)",([^\n]*)', t):
            roles = re.findall(r'RequireRole\(([^)]*)\)', m.group(2)); org = re.findall(r'RequireOrganizer\(\w+,?([^)]*)\)', m.group(2))
            if roles or org:
                r = (roles[0].replace('"', '`') if roles else "") + (" + tim: " + org[0].replace('"', '`') if org else "")
                rows.add((m.group(1), r, "ya" if "RequireMFA" in m.group(2) else "—"))
    out += [f"| `{a or '(organizer/*, grup turunan)'}` | {b} | {c} |" for a, b, c in sorted(rows)]

    # 5. enum status
    out.append("\n## 5. Enum status (CHECK constraint migrasi)\n\n| Berkas | Kolom status |\n|---|---|")
    for f in sorted(glob.glob(os.path.join(ROOT, "migrations", "*.up.sql"))):
        t = open(f).read()
        for tbl in re.finditer(r'CREATE TABLE (?:IF NOT EXISTS )?(\w+)\s*\((.*?)\n\);', t, re.S):
            for m in re.finditer(r"(\w*status\w*|kind|state) text[^\n]*?CHECK \(\w+ IN \(([^)]*)\)", tbl.group(2)):
                out.append(f"| `{tbl.group(1)}` | `{m.group(1)}`: {m.group(2).replace(chr(39), '')} |")

    # 6. knob waktu
    cfg = open(os.path.join(ROOT, "internal", "config", "config.go")).read()
    out.append("\n## 6. Knob waktu, batas, dan retensi (default di `config.go`)\n\n| Env | Default |\n|---|---|")
    for k, d in re.findall(r'(?:num|dur)\("([A-Z_]*(?:TTL|DAYS|SEC|MIN|WINDOW|LIMIT|MAX|MIN_AMOUNT|PER_MIN|STEP)[A-Z_]*)",\s*([^)\n]+)\)', cfg):
        out.append(f"| `{k}` | {d.strip()} |")
    out.append("\n## Sumber\n`internal/apperr`, `internal/service/sched.go`, `internal/handler/*.go`, `migrations/*.up.sql`, `internal/config/config.go`. Bangkitkan ulang: `python3 scripts/gen_docs.py catalog`.")
    open(os.path.join(REF, "CATALOG.md"), "w").write("\n".join(out) + "\n")


if __name__ == "__main__":
    which = sys.argv[1:] or ["api", "env", "schema", "tests", "catalog"]
    if "api" in which: api_routes()
    if "env" in which: env_doc()
    if "schema" in which: schema_doc()
    if "tests" in which: test_matrix()
    if "catalog" in which: catalog_doc()
    print("ok:", ", ".join(which))
