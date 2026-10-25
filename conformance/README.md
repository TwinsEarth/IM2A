# 关系档位门控 · 一致性测试套件（conformance）

> 协议规范：`spec/relation-gating.md` ｜ 公开向量：`spec/vectors/relation-gating.v1.json`
> 目标：**任何实现**（本仓库的 `trustzone`，或第三方的门控服务）都能用同一批向量自证符合协议。

## 1. 30 秒上手（跑本仓库的 reference 实现）

```bash
# 仓库根执行
go run ./cmd/relation-conformance -vectors spec/vectors/relation-gating.v1.json -impl reference
# 机器可读（CI/集成用）
go run ./cmd/relation-conformance -vectors spec/vectors/relation-gating.v1.json -impl reference -json
```

输出示例：

```
relation-gating conformance (spec=relation-gating/v1, impl=reference, vectors=spec/vectors/relation-gating.v1.json)
RESULT: 115/115 PASS, 0 FAIL
```

**退出码**：`0` = 全部一致；`1` = 任一向量不一致（或向量文件非法）；`2` = 参数错误（如未知 `-impl`）。\n> 注意：`go run` 会把子进程非零退出统一包装成 `1` 并打印 `exit status N`；CI 若需精确区分，先 `go build -o bin/relation-conformance ./cmd/relation-conformance` 再执行。

## 2. 接入你自己的实现（10 行）

实现一个方法即可：

```go
import "human-agent-im/conformance"

type MyGate struct{ /* 你的配置/依赖 */ }

func (g MyGate) Decide(in conformance.Input) conformance.Outcome {
    // 按 spec/relation-gating.md §3 的五步顺序判定，返回三态 + reasonCode
    return conformance.Outcome{Allowed: true, ReasonCode: conformance.ReasonOK}
}
```

然后自己写一个最小 main（不用改本仓库的 cmd）：

```go
func main() {
    vf, err := conformance.LoadVectors("spec/vectors/relation-gating.v1.json")
    if err != nil { panic(err) }
    rep := conformance.Run(MyGate{}, vf)
    b, _ := rep.JSON()
    fmt.Println(string(b))
    if rep.Failed > 0 { os.Exit(1) }
}
```

`conformance/example/stub.go` 是一个可编译的完整 stub（故意只实现最小子集，**跑向量会失败**——它演示的是接口形状，不是参考实现）。

> 第三方实现如果选择拒绝协议未定义的行为（例如负数金额），必须显式声明为扩展；v1 要求与参考行为一致（见规范 §6.2）。

## 3. 向量文件与 schema

- `spec/vectors/relation-gating.v1.json`：`{version:1, spec:"relation-gating/v1", vectors:[...]}`；
- `spec/vectors/schema.json`：JSON Schema（draft-07），第三方可用任意语言校验/消费；
- 每条向量：`id / description / category / input{capability,call} / expect{allowed,requiresApproval,reasonCode}`；
- 分类：`matrix`(35) / `order`(14) / `unknown`(7) / `boundary`(8) / `monotonic`(35) / `invariant`(14) / `purity`(2)，共 115 条。

不兼容变更 → 新文件名（`relation-gating.v2.json`），不得原地改语义。

## 4. 本仓库的 adapter

`conformance/reference.go` 把 `internal/trustzone.Capability.Check` 的
`{Allowed, RequiresApproval, Reason(人读文本)}` 映射为协议 `reasonCode`：

| 实现输出 | reasonCode |
|---|---|
| `Allowed=true` | `OK` |
| `RequiresApproval=true` + 文本 `scope ...` | `SCOPE_REQUIRES_APPROVAL` |
| `RequiresApproval=true` + 文本 `amount ...` | `AMOUNT_ABOVE_THRESHOLD` |
| deny + 文本 `skill ...` | `SKILL_NOT_ALLOWED` |
| deny + 文本 `trust ...` | `TRUST_BELOW_MIN` |
| deny + 文本 `scope ...` | `SCOPE_FORBIDDEN` |
| deny + 文本 `amount ...` | `AMOUNT_ABOVE_BUDGET` |

未知文本 → `UNKNOWN_IMPLEMENTATION_REASON`，**显式失败**而不是静默通过（防止实现漂移后测试仍然绿）。

## 5. CI

`.github/workflows/ci.yml` 的 `build-test` job 里已追加：

```yaml
- name: Relation gating conformance
  run: go run ./cmd/relation-conformance -vectors spec/vectors/relation-gating.v1.json -impl reference
```

此外 `go test ./conformance/...` 会跑同一批向量 + 分布/不变量锚点校验，二者任一失败都会挡住合并。
