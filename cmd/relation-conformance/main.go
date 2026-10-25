// cmd/relation-conformance：关系档位门控协议 v1 的一致性测试命令行。
//
// 用法（在仓库根执行）：
//
//	go run ./cmd/relation-conformance -vectors spec/vectors/relation-gating.v1.json -impl reference
//	go run ./cmd/relation-conformance -vectors spec/vectors/relation-gating.v1.json -impl reference -json
//
// 退出码：0 = 全部向量一致；1 = 有向量不一致 / 向量文件读取或校验失败；2 = 参数错误。
// 注意：`go run` 会把子进程非零退出统一包装成 1 并打印 "exit status N"；CI 若需精确区分请先 go build 再执行。
// 任何不一致都会打印逐条 want/got（人读）或输出 JSON（机器读），**绝不吞错**。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"human-agent-im/conformance"
)

func main() {
	vectorsPath := flag.String("vectors", "spec/vectors/relation-gating.v1.json", "公开向量文件路径")
	implName := flag.String("impl", "reference", "被测实现（内置：reference；第三方请用自己的 main）")
	asJSON := flag.Bool("json", false, "输出机器可读 JSON")
	verbose := flag.Bool("v", false, "逐条打印 PASS")
	flag.Parse()

	impl, err := pickImplementer(*implName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relation-conformance: %v\n", err)
		os.Exit(2)
	}

	vf, err := conformance.LoadVectors(*vectorsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relation-conformance: %v\n", err)
		os.Exit(1)
	}

	rep := conformance.Run(impl, vf)

	if *asJSON {
		out := struct {
			Implementer string              `json:"implementer"`
			Report      conformance.Report  `json:"report"`
			ExitCode    int                 `json:"exitCode"`
		}{
			Implementer: *implName,
			Report:      rep,
			ExitCode:    0,
		}
		if rep.Failed > 0 {
			out.ExitCode = 1
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "relation-conformance: marshal: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
	} else {
		fmt.Printf("relation-gating conformance (spec=%s, impl=%s, vectors=%s)\n", vf.Spec, *implName, *vectorsPath)
		for _, v := range vf.Vectors {
			got := impl.Decide(v.Input)
			if got == v.Expect {
				if *verbose {
					fmt.Printf("PASS %-42s %s\n", v.ID, got)
				}
				continue
			}
			fmt.Printf("FAIL %-42s want %s got %s  (%s)\n", v.ID, v.Expect, got, v.Description)
		}
		fmt.Printf("\nRESULT: %d/%d PASS, %d FAIL\n", rep.Passed, rep.Total, rep.Failed)
	}

	if rep.Failed > 0 {
		if !*asJSON {
			fmt.Println("FAILURES:")
			for _, f := range rep.Failures {
				fmt.Printf("  - %s: want %s got %s\n", f.ID, f.Want, f.Got)
			}
		}
		os.Exit(1)
	}
}

// pickImplementer 选择内置实现。第三方实现不需要改这个文件：
// 自己写一个 10 行 main，把 conformance.Run(yourImpl, vectors) 的结果作为退出码即可（见 conformance/README.md）。
func pickImplementer(name string) (conformance.Implementer, error) {
	switch name {
	case "reference":
		return conformance.Reference{}, nil
	default:
		return nil, fmt.Errorf("未知 -impl %q（内置仅 reference；第三方请用 conformance.Run 自定义 main）", name)
	}
}
