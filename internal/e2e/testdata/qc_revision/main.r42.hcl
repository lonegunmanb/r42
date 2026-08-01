research "source" {
  model         = "test-model"
  system_prompt = "Produce evidence that can pass QC."

  qc {
    criteria = {
      accuracy = "Every claim must have a citation."
    }
    max_qc_rounds = 2
  }
}
