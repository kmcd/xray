package github

import "testing"

func TestClassifyHarnessPath(t *testing.T) {
	cases := []struct {
		path        string
		wantTool    string
		wantKind    string
		wantMatched bool
	}{
		{"CLAUDE.md", "claude_code", "instructions", true},
		{".claude/rules/foo.md", "claude_code", "rules", true},
		{".claude/skills/bar.md", "claude_code", "skills", true},
		{".claude/agents/baz.md", "claude_code", "agents", true},
		{".claude/commands/qux.md", "claude_code", "commands", true},
		{".claude/something.md", "claude_code", "instructions", true},
		{"AGENTS.md", "unknown", "instructions", true},
		{".cursorrules", "cursor", "rules", true},
		{".cursor/rules", "cursor", "rules", true},
		{".cursor/rules/foo.md", "cursor", "rules", true},
		{".github/copilot-instructions.md", "copilot", "instructions", true},
		{".aiderconf", "aider", "rules", true},
		{".aider.conf.yml", "aider", "rules", true},
		{"aider.conf.yml", "aider", "rules", true},
		{".windsurfrules", "windsurf", "rules", true},
		{".continue/config.json", "continue", "rules", true},
		{".mcp.json", "generic_mcp", "mcp", true},
		{"sub/mcp.json", "generic_mcp", "mcp", true},
		// workflow candidate: tool empty until content-sniffed
		{".github/workflows/ci.yml", "", "workflow", true},
		{".github/workflows/ci.yaml", "", "workflow", true},
		// non-matches
		{"README.md", "", "", false},
		{"src/main.go", "", "", false},
		{".github/workflows/ci.txt", "", "", false},
		{".github/dependabot.yml", "", "", false},
	}
	for _, c := range cases {
		tool, kind, matched := classifyHarnessPath(c.path)
		if matched != c.wantMatched || tool != c.wantTool || kind != c.wantKind {
			t.Errorf("classifyHarnessPath(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.path, tool, kind, matched, c.wantTool, c.wantKind, c.wantMatched)
		}
	}
}

func TestDetectAIBotInWorkflow(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantTool string
		wantOK   bool
	}{
		{"coderabbit", "uses: coderabbitai/action@v1", "coderabbit", true},
		{"claude", "uses: anthropics/claude-code-action@v1", "claude_code", true},
		{"copilot", "name: Copilot review", "copilot", true},
		{"cursor", "run: cursor-agent --help", "cursor", true},
		{"plain ci", "name: tests\non: push", "", false},
		{"case insensitive", "USES: CodeRabbitAI/action", "coderabbit", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool, ok := detectAIBotInWorkflow([]byte(c.content))
			if ok != c.wantOK || tool != c.wantTool {
				t.Errorf("detectAIBotInWorkflow(%q) = (%q, %v), want (%q, %v)",
					c.content, tool, ok, c.wantTool, c.wantOK)
			}
		})
	}
}

func TestCountLines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"\n", 1},
		{"\n\n", 2},
	}
	for _, c := range cases {
		got := countLines([]byte(c.in))
		if got != c.want {
			t.Errorf("countLines(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestIsWorkflowPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".github/workflows/ci.yml", true},
		{".github/workflows/release.yaml", true},
		{".github/workflows/nested/foo.yml", true},
		{".github/workflows/README.md", false},
		{".github/dependabot.yml", false},
		{"workflows/ci.yml", false},
	}
	for _, c := range cases {
		if got := isWorkflowPath(c.path); got != c.want {
			t.Errorf("isWorkflowPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
