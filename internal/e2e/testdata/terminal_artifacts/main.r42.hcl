go_tool "finish" {
  description = "Finish research"
  source = <<-GO
    import "context"

    type Input struct {
      Summary string
    }

    type Output string

    func Invoke(_ context.Context, input Input) (ToolResponse[Output], error) {
      output := Output(input.Summary)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }
  GO
}

research "static" "source" {
  model          = "test-model"
  system_prompt  = "Write the required report and evidence."
  terminate_tool_id = go_tool.finish.id

  artifact "report" {
    type      = "file"
    path      = "report.md"
	description = "Research report fixture"
    required  = true
    non_empty = true
  }

  artifact "evidence" {
    type      = "directory"
    path      = "evidence"
	description = "Evidence directory fixture"
    required  = true
    non_empty = true
  }
}

output "summary" {
  value = research.static.source.result
}

output "report_path" {
  value = research.static.source.artifact.report.path
}

output "evidence_path" {
  value = research.static.source.artifact.evidence.path
}
