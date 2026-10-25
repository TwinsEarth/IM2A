// Package store 的 schema 契约测试（不需要 PG，纯静态分析）。
//
// 存在意义（2026-10-25，task-11）：
//   仓库曾出现 9 张「代码在读写、迁移里从未建过」的表（devices / im_groups /
//   im_group_members / im_webhook_events / ticket_state / offline_approvals /
//   trust_relations / blacklist / payment_ledger_legacy_v09）。它们被**测试夹具
//   自建 DDL 掩盖**（multidevice_test.go:28-41 手工 CREATE TABLE），CI 全绿而生产
//   SQL 全部 42P01，其中 im_webhook_events 缺失还会让全新 PG 库在 004_im.sql 的
//   ALTER TABLE 处失败，导致 配置了 PG_DSN 的服务无法启动。
//
// 本测试把「迁移声明的表集合」与「源码里真实出现的表名」做双向断言，防止同类漂移：
//   1) 源码引用的每张表都必须在迁移里声明（例外必须显式登记在 knownUndeclared 并写原因）；
//   2) privacy 包的数据主体权利表清单（导出/删除白名单）必须全部声明；
//   3) 动态表名（fmt.Sprintf("... FROM %s")）只允许出现在 privacy 的白名单渲染处，
//      避免出现静态分析看不见的第三类漂移；
//   4) 迁移解析结果不得突然变小（防止正则/文件结构变更把测试变成永远绿）。
//   5) 序列（nextval）同样做差集：CREATE SEQUENCE 不在表解析范围内，曾漏掉 seq_user_id
//      （v2.3.28 真库首验缺陷 3；表都建了但 identify 仍 42P01）。
package store

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// knownUndeclared：源码引用但**故意**不由迁移建的表 —— 每一条都必须写清原因。
// 空 map 表示"零容忍"。新增条目等于在代码里留一个已知的生产错误，评审时必须问为什么。
// v2.3.27（task-12）起为**空**：payment_ledger.go 已改写到 paid_message_ledger，
// 全仓对退役表 payment_ledger_legacy_v09 的引用归零。若将来有人把代码改回去，
// 本测试会立即变红（这就是这条白名单存在的意义：异常必须显式登记）。
var knownUndeclared = map[string]string{}

// declaredCommentsOnly 防御：注释里出现的 "CREATE TABLE xxx" 不算声明（解析前先剥 -- 注释）。

// TestSchemaContract_MigrationsDeclareEveryReferencedTable 是全量差集断言。
func TestSchemaContract_MigrationsDeclareEveryReferencedTable(t *testing.T) {
	declared := declaredTablesFromMigrations(t)
	if len(declared) < 50 {
		t.Fatalf("迁移声明表只有 %d 张（<50），解析器很可能失效了：%v", len(declared), sortedKeys(declared))
	}

	refs := referencedTablesFromSources(t)
	if len(refs) < 20 {
		t.Fatalf("源码里只提取到 %d 个表引用（<20），扫描器很可能失效", len(refs))
	}

	var missing []string
	for table := range refs {
		if declared[table] {
			continue
		}
		if _, ok := knownUndeclared[table]; ok {
			continue
		}
		missing = append(missing, table)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		var b strings.Builder
		for _, table := range missing {
			b.WriteString("\n  - ")
			b.WriteString(table)
			b.WriteString("  ← 引用点: ")
			b.WriteString(strings.Join(refs[table], ", "))
		}
		t.Fatalf("发现「代码有、迁移无」的表 %d 张：%s\n"+
			"修复方式：在 internal/store/migrations/ 下新增幂等迁移（CREATE TABLE IF NOT EXISTS），"+
			"或把代码改到已声明的表上；不要靠测试夹具建表来掩盖。", len(missing), b.String())
	}
}

// TestSchemaContract_SequencesReferencedAreDeclared 守住「代码 nextval 的序列，迁移必须先
// CREATE SEQUENCE」。表差集测试只扫 CREATE TABLE，看不见序列；而 v2.3.28 真库首验缺陷 3
// 恰恰是：internal/wechatid/wechatid.go:27-28 与 internal/api/wechat_webhook.go:216-217
// 调 `nextval('seq_user_id')`，但 001~038 没有任何迁移创建该序列 → 全新库 identify 直接
// 42P01。本测试把「迁移声明的序列集合」与「源码里真实 nextval 的序列名」做差集断言。
func TestSchemaContract_SequencesReferencedAreDeclared(t *testing.T) {
	declared := declaredSequencesFromMigrations(t)
	if len(declared) == 0 {
		t.Fatalf("迁移里一个 CREATE SEQUENCE 都没解析到，解析器或迁移结构变了")
	}
	refs := referencedSequencesFromSources(t)
	if len(refs) == 0 {
		t.Fatalf("源码里一个 nextval('...') 都没解析到，扫描器很可能失效")
	}
	var missing []string
	for seq := range refs {
		if declared[seq] {
			continue
		}
		missing = append(missing, seq+"  ← 引用点: "+strings.Join(refs[seq], ", "))
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("发现「代码有 nextval、迁移无 CREATE SEQUENCE」的序列 %d 个：\n  - %s\n"+
			"修复方式：新增幂等迁移 CREATE SEQUENCE IF NOT EXISTS（参考 040_seq_user_id.sql）；"+
			"不要靠测试夹具建序列来掩盖。",
			len(missing), strings.Join(missing, "\n  - "))
	}
	// 反向抽样：本轮修复的 seq_user_id 必须在声明集合里，防止迁移被误删后测试仍绿。
	if !declared["seq_user_id"] {
		t.Fatalf("迁移声明序列里缺 seq_user_id：%v", sortedSetKeys(declared))
	}
}

// TestSchemaContract_PrivacyWhitelistDeclared privacy 的白名单是**动态 SQL** 的表名来源，
// 静态扫描看不到，因此单独断言：白名单里的每一张表都必须真实存在。
func TestSchemaContract_PrivacyWhitelistDeclared(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "internal", "privacy", "privacy.go"))
	if err != nil {
		t.Fatalf("read privacy.go: %v", err)
	}
	declared := declaredTablesFromMigrations(t)

	// 形如 {"identity", "devices", "user_id", true},
	re := regexp.MustCompile(`\{\s*"[^"]*"\s*,\s*"([a-z_][a-z0-9_]*)"\s*,\s*"([a-z_][a-z0-9_]*)"\s*,\s*(?:true|false)\s*\}`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	if len(matches) < 20 {
		t.Fatalf("privacy 白名单只解析到 %d 条（<20），正则或结构变了", len(matches))
	}
	checked := map[string]bool{}
	for _, m := range matches {
		table := m[1]
		if checked[table] {
			continue
		}
		checked[table] = true
		if !declared[table] {
			t.Errorf("privacy 白名单引用了未声明的表 %q（列 %q）：导出/被遗忘权会 42P01", table, m[2])
		}
	}
	if len(checked) < 15 {
		t.Fatalf("privacy 去重后只检查了 %d 张表（<15）", len(checked))
	}
}

// TestSchemaContract_NoUnsanctionedDynamicTableNames 守住"动态表名"这条缝：
// 目前唯一允许动态拼表名的地方是 internal/privacy（表名来自上面的白名单常量）。
// 其他文件里出现 `FROM %s` / `INTO %s` / `UPDATE %s` 一律拒绝，因为静态分析看不见。
func TestSchemaContract_NoUnsanctionedDynamicTableNames(t *testing.T) {
	root := repoRoot(t)
	dyn := regexp.MustCompile(`(?i)\b(from|into|update|join)\s+%s`)
	allowed := filepath.Join("internal", "privacy", "privacy.go")

	var bad []string
	walkGoSources(t, root, func(rel string, body string) {
		if rel == allowed {
			return
		}
		// 只看生产代码：测试夹具有权对"已声明的表"做动态清理 SQL（且不构成生产依赖）。
		if strings.HasSuffix(rel, "_test.go") {
			return
		}
		for _, line := range strings.Split(body, "\n") {
			if dyn.MatchString(line) {
				bad = append(bad, rel+": "+strings.TrimSpace(line))
			}
		}
	})
	if len(bad) > 0 {
		t.Fatalf("发现未登记的动态表名 SQL（静态分析无法覆盖）：\n  %s\n"+
			"如确需动态表名，请把它收敛到有白名单常量的位置，并在本测试里登记。",
			strings.Join(bad, "\n  "))
	}
}

// TestSchemaContract_AlterAndIndexTargetsDeclaredBefore 守住「迁移顺序依赖」这一类 bug：
// im_webhook_events 之所以能让全新库起不来，就是因为 004_im.sql 对一张从未建过的表做
// ALTER TABLE / CREATE INDEX —— 它必须先于 004 被建出来（故本仓用 003a_ 插队）。
// 规则：按文件名字典序（migrate.go:41 的执行顺序）逐个文件检查，ALTER TABLE / CREATE INDEX
// 的目标表必须在此前的文件里声明过，或在**同一文件内更早**声明。
func TestSchemaContract_AlterAndIndexTargetsDeclaredBefore(t *testing.T) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	reCreate := regexp.MustCompile(`(?i)create\s+table\s+(?:if\s+not\s+exists\s+)?([a-z_][a-z0-9_]*)`)
	reAlter := regexp.MustCompile(`(?i)alter\s+table\s+(?:if\s+exists\s+)?(?:only\s+)?([a-z_][a-z0-9_]*)`)
	reIdx := regexp.MustCompile(`(?i)create\s+(?:unique\s+)?index(?:[\s\S]{0,200}?)\son\s+([a-z_][a-z0-9_]*)`)

	declared := map[string]bool{}
	sorted := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			sorted = append(sorted, e.Name())
		}
	}
	sort.Strings(sorted) // 与 migrate.go 的 sort.Strings 保持一致

	for _, name := range sorted {
		raw, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := stripSQLComments(string(raw))
		fileCreates := map[string]bool{}
		for _, m := range reCreate.FindAllStringSubmatch(body, -1) {
			fileCreates[strings.ToLower(m[1])] = true
		}
		for _, re := range []*regexp.Regexp{reAlter, reIdx} {
			for _, m := range re.FindAllStringSubmatch(body, -1) {
				target := strings.ToLower(m[1])
				if declared[target] || fileCreates[target] {
					continue
				}
				t.Errorf("%s 对表 %q 执行 ALTER/INDEX，但该表既未在此前迁移中声明、也未在本文件内声明"+
					"（迁移按文件名字典序执行，见 migrate.go:41；全新库会 42P01 失败）", name, target)
			}
		}
		for table := range fileCreates {
			declared[table] = true
		}
	}
}

var reLineComment = regexp.MustCompile(`--[^\n]*`)

// stripSQLComments 去掉 `--` 行注释，避免注释里出现的 DDL 片段被当成真实语句。
func stripSQLComments(s string) string {
	return reLineComment.ReplaceAllString(s, "")
}

// ---- 解析器 ----

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0) // .../internal/store/schema_contract_test.go
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// declaredTablesFromMigrations 从内嵌迁移文件里提取 CREATE TABLE 的表名（先剥 -- 注释）。
func declaredTablesFromMigrations(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	reCreate := regexp.MustCompile(`(?i)create\s+table\s+(?:if\s+not\s+exists\s+)?([a-z_][a-z0-9_]*)`)
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			for _, m := range reCreate.FindAllStringSubmatch(line, -1) {
				out[strings.ToLower(m[1])] = true
			}
		}
	}
	return out
}

// referencedTablesFromSources 扫描 internal/ 与 services/ 下的非测试 Go 源码，
// 提取 SQL 里的表名 → 引用点列表。刻意过滤两类噪声：
//   - EXTRACT(EPOCH FROM created_at) 这类 "FROM <列名>"；
//   - SQL 关键字/函数名被误当表名。
func referencedTablesFromSources(t *testing.T) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	reStmt := regexp.MustCompile(`(?i)\b(?:insert\s+into|delete\s+from|update|from|join|truncate(?:\s+table)?)\s+([a-z_][a-z0-9_]*)`)
	reSQLLine := regexp.MustCompile(`(?i)\b(select|insert|update|delete|alter|create|truncate|join|with)\b`)
	reExtract := regexp.MustCompile(`(?i)extract\s*\([^)]*$`)

	// 列名/函数名/关键字噪声：这些不是表。
	noise := map[string]bool{
		"set": true, "values": true, "where": true, "select": true, "into": true,
		"dual": true, "lateral": true, "unnest": true, "generate_series": true,
		"created_at": true, "updated_at": true, "now": true, "epoch": true,
		"jsonb_array_elements": true, "row": true, "only": true, "table": true,
		// "FOR UPDATE SKIP LOCKED" 会被 update\s+ 规则误抓成表名 "skip"。
		"skip": true,
	}

	out := map[string][]string{}
	walkGoSources(t, root, func(rel, body string) {
		if strings.HasSuffix(rel, "_test.go") {
			return // 测试夹具自建 DDL 不算"生产依赖"，否则本测试永远无法发现缺失
		}
		for ln, line := range strings.Split(body, "\n") {
			if !reSQLLine.MatchString(line) {
				continue
			}
			for _, m := range reStmt.FindAllStringSubmatchIndex(line, -1) {
				table := strings.ToLower(line[m[2]:m[3]])
				if noise[table] {
					continue
				}
				// EXTRACT(EPOCH FROM created_at) —— "FROM" 前是 EXTRACT(... 的未闭合括号
				if reExtract.MatchString(strings.ToLower(line[:m[0]])) {
					continue
				}
				out[table] = append(out[table], rel+":"+itoa(ln+1))
			}
		}
	})
	return out
}

// walkGoSources 遍历 internal/ 与 services/ 下的 .go 文件（跳过 vendor 与 migrations 目录）。
func walkGoSources(t *testing.T, root string, fn func(rel string, body string)) {
	t.Helper()
	for _, dir := range []string{"internal", "services"} {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "vendor" || d.Name() == "migrations" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			fn(filepath.ToSlash(rel), string(body))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedSetKeys 与 sortedKeys 同义（序列断言里用更明确的名字）。
func sortedSetKeys(m map[string]bool) []string { return sortedKeys(m) }

// declaredSequencesFromMigrations 从内嵌迁移里提取 CREATE SEQUENCE 的序列名（先剥 -- 注释）。
func declaredSequencesFromMigrations(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	reCreate := regexp.MustCompile(`(?i)create\s+sequence\s+(?:if\s+not\s+exists\s+)?([a-z_][a-z0-9_]*)`)
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			for _, m := range reCreate.FindAllStringSubmatch(line, -1) {
				out[strings.ToLower(m[1])] = true
			}
		}
	}
	return out
}

// referencedSequencesFromSources 扫描 internal/ 与 services/ 非测试源码里的 nextval('序列名')。
func referencedSequencesFromSources(t *testing.T) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	re := regexp.MustCompile(`(?i)nextval\s*\(\s*'([A-Za-z_][A-Za-z0-9_.]*)'`)
	out := map[string][]string{}
	walkGoSources(t, root, func(rel, body string) {
		if strings.HasSuffix(rel, "_test.go") {
			return
		}
		for ln, line := range strings.Split(body, "\n") {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				name := strings.ToLower(m[1])
				if i := strings.LastIndex(name, "."); i >= 0 { // 允许 schema 限定名
					name = name[i+1:]
				}
				out[name] = append(out[name], rel+":"+itoa(ln+1))
			}
		}
	})
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
