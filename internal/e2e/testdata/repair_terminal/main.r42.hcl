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

research "source" {
  model                 = "test-model"
  system_prompt         = "Repair invalid terminal arguments and finish."
  terminate_tool        = go_tool.finish
  max_protocol_attempts = 3
}

output "summary" {
  value = research.source.result
}
