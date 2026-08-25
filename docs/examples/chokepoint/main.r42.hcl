module "pplx_tools" {
  source = "./modules/pplx_tools"
}

research "static" "primary_source_baseline" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    You establish the authoritative primary-source baseline for a supply-chain
    study. Keep each claim atomic and time bounded. Confirm only what an official
    filing, regulator record, official product document, or official statement
    directly says. Never infer an unnamed counterparty.

    Never use PowerShell, a shell, curl, wget, scripts, or command-line programs
    to search or download content. Stop searching when the newest relevant
    primary corpus is represented and remaining gaps are explicit.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity artifact_dir: %s", artifact("sources").path) : ""}
    Workspace: "${block_wd()}"

    During Collection, find the newest official filings, regulator records,
    product specifications, customer or contract disclosures, and applicable
    rules available by the cutoff. Save every retained source as a Markdown
    artifact at a unique .md path under "${artifact("sources").path}" by calling r42_save_artifact,
    then submit a collection checkpoint when the primary
    corpus is sufficient.

    During closed Research, do not search or fetch. Use the authorized artifact_id
    values supplied to this phase with r42_read_artifact. Register each retained
    source with ${go_tool.register_evidence_source.id}, using that artifact_id
    and workspace_dir "${block_wd()}".

    Submit atomic cards in batches of at most five with
    ${go_tool.submit_claim_cards.id}. During revision, use remove_claim_ids in
    the same call to remove an earlier staged card. Use confirmed only when an authoritative
    primary source directly states the claim. Use reported for direct published
    reporting. A direct card must use the same artifact_id as its registered
    source. Use inferred only for a conclusion derived from existing card IDs;
    inferred cards use derived_from and no source_id, artifact_id, quote, or
    locator. Do not submit unknown as a claim card.

    At the end of each Research pass, call
    ${go_tool.finalize_claim_cards.id} exactly once:
    workspace_dir "${block_wd()}", claims_path "${artifact("claims").path}",
    source_registry_path "${artifact("source_registry").path}", as_of_date
    "${var.as_of_date}", and allow_empty false. An accepted call completes only
    that pass. If Final QC returns this block to Research, revise the staged
    cards and call finalize again in the later pass.
  PROMPT
  collection_tool_ids = local.pplx_tool_ids
  artifact "sources" {
    type        = "directory"
    path        = "${block_wd()}/artifacts/sources"
    description = "Primary-source material collected for the baseline."
  }
  tool_use "register_source" {
    tool_id = go_tool.register_evidence_source.id
    input = {
      workspace_dir = block_wd()
    }
  }
  tool_use "submit_claims" {
    tool_id = go_tool.submit_claim_cards.id
    input = {
      workspace_dir = block_wd()
      claims_path   = "${artifact("claims").path}"
    }
  }
  tool_use "finalize_claims" {
    tool_id   = go_tool.finalize_claim_cards.id
    terminate = true
    input = {
      workspace_dir        = block_wd()
      claims_path           = "${artifact("claims").path}"
      source_registry_path  = "${artifact("source_registry").path}"
      as_of_date            = var.as_of_date
      allow_empty           = false
    }
  }
  tool_call_quota   = local.pplx_tool_call_quota
  disallowed_tools  = local.research_disallowed_tools
  permission        = "approve_all"

  artifact "claims" {
    type      = "file"
    path      = "${block_wd()}/claims.json"
	description = "Validated atomic baseline claim cards with evidence references"
    required  = true
    non_empty = true
  }
  artifact "source_registry" {
    type      = "file"
    path      = "${block_wd()}/source-registry.json"
	description = "Baseline source metadata indexed by registered artifact ID"
    required  = true
    non_empty = true
  }

  collection_qc {
    model_provider = model_provider.qc
    criteria = {
      primary_coverage = "For each active information need and stop condition, judge whether the newest material primary documents available by the cutoff were retained and directly establish the required coverage. Mark unmet condition IDs needs_more and identify every omission that could change the study."
    }
  }

  qc {
    criteria = {
      entailment = "Judge whether every confirmed card is directly entailed by its authoritative source, including named parties, product variant, period, and qualifiers. Treat typed-tool path, URL, date, and quotation matching as authoritative."
      atomicity = "Judge whether each card makes one independently auditable assertion rather than bundling several facts behind one citation."
    }
    model_provider   = model_provider.qc
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "static" "brainstorm" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    Define a coverage-complete product and supply-chain scope before judging any
    risk. Keep generic architecture separate from facts about the named target.
    Do not select companies or chokepoints.

    Never use PowerShell, a shell, curl, wget, scripts, or command-line programs
    to search or download content. Only configured source tools may access the web.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    Validated primary-source baseline JSON:
    ${research.static.primary_source_baseline.result}
    ${local.source_tool_guidance}
    ${var.use_pplx ? format("Perplexity artifact_dir: %s", artifact("sources").path) : ""}

    During Collection, acquire only evidence needed beyond the validated
    baseline. Save every retained source at a unique .md path under
    "${artifact("sources").path}" with r42_save_artifact and submit a collection checkpoint. If the baseline
    is sufficient for scope design, submit an empty collection checkpoint.

    During closed Research, do not search or fetch. Use the authorized artifact_id
    values supplied to this phase with r42_read_artifact, and use the validated
    Write "${artifact("brainstorm").path}" with the
    focal boundary, product variants, layered components, transformation stages,
    manufacturing, packaging, testing, module integration, downstream system
    qualification, competing dependency hypotheses, and open questions for the
    five tracks.

    Stop upstream decomposition at production equipment and input materials.
    Continue downstream through module components and system qualification.
    Then call ${go_tool.submit_supply_chain_scope.id}; r42 binds its declared
    scope artifact_id. Every expected component and stage must be
    assigned to one or more coverage items and exactly one of the five tracks.
  PROMPT
  collection_tool_ids = local.pplx_tool_ids
  artifact "sources" {
    type        = "directory"
    path        = "${block_wd()}/artifacts/sources"
    description = "Additional source material used to define study scope."
  }
  import_artifact "baseline" {
    desc    = "Validated baseline claims and source registry used to define the scope."
    sources = values(research.static.primary_source_baseline.artifact)
  }
  tool_use "submit_scope" {
    tool_id   = go_tool.submit_supply_chain_scope.id
    terminate = true
    input = {
      artifact_id             = artifact("scope").id
      _r42_artifact_path      = ""
      topic                   = var.topic
    }
    input_from_agent = {
      focal_product = {
        desc = "The exact focal product or system boundary."
        sources = values(research.static.primary_source_baseline.artifact)
      }
      product_variants = {
        desc = "Material product variants and branches."
        sources = values(research.static.primary_source_baseline.artifact)
      }
      expected_components = {
        desc = "Expected components within the declared boundary."
        sources = values(research.static.primary_source_baseline.artifact)
      }
      expected_stages = {
        desc = "Expected manufacturing, testing, qualification, and integration stages."
        sources = values(research.static.primary_source_baseline.artifact)
      }
      upstream_boundaries = {
        desc = "The upstream boundary of the study."
        sources = values(research.static.primary_source_baseline.artifact)
      }
      downstream_boundary = {
        desc = "The downstream boundary of the study."
        sources = values(research.static.primary_source_baseline.artifact)
      }
      coverage_items = {
        desc = "Coverage items assigned to one of the five track values listed in the tool description."
        sources = values(research.static.primary_source_baseline.artifact)
      }
      open_questions = {
        desc = "Material scope questions that remain unresolved."
        sources = values(research.static.primary_source_baseline.artifact)
      }
    }
  }
  tool_call_quota   = local.pplx_tool_call_quota
  disallowed_tools  = local.research_disallowed_tools
  permission        = "approve_all"

  artifact "brainstorm" {
    type      = "file"
    path      = "${block_wd()}/brainstorm.md"
	description = "Narrative product boundary and supply-chain scope analysis"
    required  = true
    non_empty = true
  }
  artifact "scope" {
    type      = "file"
    path      = "${block_wd()}/scope.json"
	description = "Structured component, stage, branch, and research-track scope"
    required  = true
    non_empty = true
  }

  collection_qc {
    model_provider = model_provider.qc
  }

  qc {
    criteria = {
      scope = "Judge whether every decision-relevant product branch, component, stage, equipment or material boundary, service dependency, and qualification step is represented."
      separation = "Judge whether generic architecture is clearly separated from target-specific facts and materially different variants are not silently merged."
      uncertainty = "Judge whether competing explanations and material open questions remain explicit."
    }
    model_provider   = model_provider.qc
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "dynamic" "graph_track" {
  tasks = [
    for index, track_key in keys(local.graph_tracks) : {
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt    = var.research_system_prompt
      prompt = <<-PROMPT
        Topic: ${var.topic}
        Evidence cutoff: ${var.as_of_date}
        Track: ${local.graph_tracks[track_key].title}
        Assigned question: ${local.graph_tracks[track_key].question}
        Validated scope JSON:
        ${research.static.brainstorm.result}
        Validated primary-source baseline JSON:
        ${research.static.primary_source_baseline.result}
        ${local.source_tool_guidance}
        ${var.use_pplx ? format("Perplexity artifact_dir: %s", artifact("sources").path) : ""}
        Workspace: "${block_wd()}/${index}"

        During Collection, research only this track and address every assigned
        coverage item. Preserve product variants and distinguish generic-chain facts
        from target facts. Save every retained source at a unique .md path under
        "${artifact("sources").path}" with r42_save_artifact, then submit a
        collection checkpoint when sufficient.

        During closed Research, do not search or fetch. Use the authorized artifact_id
        values supplied to this phase with r42_read_artifact. Register every retained
        source with ${go_tool.register_evidence_source.id}, using that artifact_id.
        Direct claim
        cards must use the same artifact_id as their registered source.

        Submit one independently auditable claim per card with
        ${go_tool.submit_claim_cards.id}, in batches of at most five. During
        revision, use remove_claim_ids in the same call to remove an earlier
        staged card. Prefix IDs
        with "${track_key}-". Use confirmed, reported, and inferred exactly as
        described by the tool; record unresolved questions in the track narrative,
        not as fake claims.

        At the end of each Research pass, call
        ${go_tool.finalize_claim_cards.id} exactly once: workspace_dir
        "${block_wd()}/${index}", claims_path
        "${artifact("claims").path}", source_registry_path
        "${artifact("source_registry").path}", as_of_date
        "${var.as_of_date}", and allow_empty false. An accepted call completes
        only that pass. If Final QC returns this block to Research, revise the
        staged cards and call finalize again in the later pass.
      PROMPT
      collection_tool_ids = local.pplx_tool_ids
      import_artifact = {
        baseline = {
          desc    = "Validated baseline claims and source registry for this track."
          sources = values(research.static.primary_source_baseline.artifact)
        }
        scope = {
          desc    = "Validated product scope and coverage items for this track."
          sources = values(research.static.brainstorm.artifact)
        }
      }
      tool_use = {
        register_source = {
          tool_id = go_tool.register_evidence_source.id
          input = {
            workspace_dir = "${block_wd()}/${index}"
          }
        }
        submit_claims = {
          tool_id = go_tool.submit_claim_cards.id
          input = {
            workspace_dir = "${block_wd()}/${index}"
            claims_path   = artifact("claims").path
          }
          input_from_agent = {
            cards = {
              desc = "Atomic claim cards for this track, grounded in current registered sources and the validated upstream scope and baseline."
              sources = concat(
                values(research.static.primary_source_baseline.artifact),
                values(research.static.brainstorm.artifact),
              )
            }
          }
        }
        finalize_claims = {
          tool_id = go_tool.finalize_claim_cards.id
          terminate = true
          input = {
            workspace_dir       = "${block_wd()}/${index}"
            claims_path         = artifact("claims").path
            source_registry_path = artifact("source_registry").path
            as_of_date          = var.as_of_date
            allow_empty         = false
          }
        }
      }
      tool_call_quota   = local.pplx_tool_call_quota
      disallowed_tools  = local.research_disallowed_tools
      permission        = "approve_all"
      artifact = {
        sources = {
          type        = "directory"
          path        = "${block_wd()}/${index}/artifacts/sources"
          description = "Source material collected for this graph track."
        }
        claims = {
          type      = "file"
          path      = "${block_wd()}/${index}/claims.json"
		      description = "Validated atomic claims for this supply-chain track"
          required  = true
          non_empty = true
        }
        source_registry = {
          type      = "file"
          path      = "${block_wd()}/${index}/source-registry.json"
		      description = "Source metadata for this track's registered evidence artifacts"
          required  = true
          non_empty = true
        }
      }
      retry = null
      collection_qc = {
        model_provider = model_provider.qc
      }
      qc = {
        criteria = {
          track_scope = "Judge whether the cards answer this track's assigned questions and preserve material unknowns without drifting into company selection."
          entailment = "Judge whether each source actually entails its atomic claim with the stated party, period, product branch, and qualifier. Treat typed-tool schema, path, URL, date, and quote matching as authoritative."
          inference = "Judge whether inferred cards follow from their premise cards without silently upgrading correlation, supplier marketing, or general industry structure into a target-specific fact."
        }
        model_provider   = model_provider.qc
        model            = local.qc_model
        reasoning_effort = var.reasoning_effort
        max_qc_rounds    = var.max_qc_rounds
        disallowed_tools = local.semantic_qc_disallowed_tools
        permission       = "approve_all"
        retry            = null
      }
    }
  ]
}

research "static" "build_supply_chain" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    Build a readable reference supply-chain map from accepted scope and atomic
    claim cards. Do not score, rank, or pre-declare chokepoints. Select only a
    short list of nodes whose continuity risk genuinely warrants assessment.
    Companies are not supply-chain nodes.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Validated scope JSON:
    ${research.static.brainstorm.result}
    Validated baseline and track claim JSON:
    ${research.static.primary_source_baseline.result}
    ${join("\n", [for item in research.dynamic.graph_track.tasks : item.result])}

    During Collection, do not acquire new evidence. The validated JSON above is
    complete for this stage, so submit an empty collection checkpoint.

    During each Research pass, use only the validated JSON above and call
    ${go_tool.submit_supply_chain_map.id} one time. r42 binds its declared
    supply-chain artifact_id, workspace_dir, exact topic and scope_path, and
    claim_paths containing the baseline plus all five track claim files.
    If Final QC returns this block to Research, revise and submit the map again
    in the later pass.

    Map ordinary nodes and edges across the full declared product boundary.
    Keep each node's stages and product branches explicit. Attach only claim IDs
    that substantively support the node or edge. Put evidence gaps in unknowns.
    assessment_targets is not a chokepoint list: include only nodes for which the
    next stage should separately test actual target dependency, alternatives,
    switching versus buffer time, applicable scenario, and falsification.
    Separately select company_mapping_targets only from supplier-addressable
    component, material, process, equipment, or service nodes where a later
    stage can test public-company relationships or exact-node capabilities.
    A company-mapping target need not be a continuity-risk assessment target.
  PROMPT
  import_artifact "baseline" {
    desc    = "Validated baseline claims used to build the supply-chain map."
    sources = values(research.static.primary_source_baseline.artifact)
  }
  import_artifact "scope" {
    desc    = "Validated scope definition used to build the supply-chain map."
    sources = values(research.static.brainstorm.artifact)
  }
  import_artifact "track_claims" {
    desc    = "Validated graph-track claims used to build the supply-chain map."
    sources = flatten([for task in research.dynamic.graph_track.tasks : values(task.artifact)])
  }
  tool_use "submit_supply_chain_map" {
    tool_id   = go_tool.submit_supply_chain_map.id
    terminate = true
    input = {
      workspace_dir        = block_wd()
      artifact_id          = artifact("supply_chain").id
      _r42_artifact_path   = ""
      topic                = var.topic
      scope_path    = research.static.brainstorm.artifact.scope.path
      claim_paths = concat(
        [research.static.primary_source_baseline.artifact.claims.path],
        [for task in research.dynamic.graph_track.tasks : task.artifact.claims.path],
      )
    }
    input_from_agent = {
      nodes = {
        desc = "Reference supply-chain nodes with their stages, branches, claim IDs, and unknowns."
        sources = flatten([values(research.static.brainstorm.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
      }
      edges = {
        desc = "Reference supply-chain edges with their relation and supporting claim IDs."
        sources = flatten([values(research.static.brainstorm.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
      }
      assessment_targets = {
        desc = "Nodes that warrant a separate continuity-risk assessment."
        sources = flatten([values(research.static.brainstorm.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
      }
      company_mapping_targets = {
        desc = "Supplier-addressable component, material, process, equipment, or service nodes worth mapping to public companies. Each target has node_id, exact node_name, why_map, and supporting claim_ids; this list is independent of continuity-risk assessment_targets."
        sources = flatten([values(research.static.brainstorm.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
      }
      unknowns = {
        desc = "Evidence gaps and unresolved map questions."
        sources = flatten([values(research.static.brainstorm.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
      }
    }
  }
  disallowed_tools  = local.offline_disallowed_tools
  permission        = "approve_all"

  artifact "supply_chain" {
    type      = "file"
    path      = "${block_wd()}/supply-chain.json"
	description = "Structured supply-chain nodes, edges, continuity assessments, and company-mapping targets"
    required  = true
    non_empty = true
  }

  collection_qc {
    model_provider = model_provider.qc
  }

  qc {
    criteria = {
      completeness = "Judge whether the map covers the decision-relevant product branches and stages defined by scope without becoming an encyclopedia."
      target_selection = "Judge whether every assessment target could plausibly affect continuity and whether important candidates were omitted. A required input or commercially attractive supplier is not automatically a risk node."
      company_mapping = "Judge whether company_mapping_targets cover decision-relevant supplier-addressable nodes without conflating them with continuity-risk assessment_targets or putting companies into the graph."
      evidence = "Judge whether cited claims semantically support the mapped relationship and target rationale. Treat typed-tool graph references as authoritative."
    }
    model_provider   = model_provider.qc
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

research "dynamic" "assess_nodes" {
  tasks = [
    for index, target in jsondecode(research.static.build_supply_chain.result).assessment_targets : {
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt = <<-PROMPT
        Assess one supply-chain node as a falsifiable buyer continuity-risk
        question. Scope and proof strength are independent dimensions. Do not
        search, score companies, or turn uncertainty into a conclusion.
      PROMPT
      prompt = <<-PROMPT
        Topic: ${var.topic}
        Evidence cutoff: ${var.as_of_date}
        Assessment target: ${jsonencode(target)}
        Validated supply-chain map JSON:
        ${research.static.build_supply_chain.result}
        Validated claim JSON:
        ${research.static.primary_source_baseline.result}
        ${join("\n", [for item in research.dynamic.graph_track.tasks : item.result])}

        During Collection, do not acquire new evidence. The validated map and
        claims are complete for this stage, so submit an empty collection checkpoint.

        During closed Research, use the included map and claims. Answer five questions: which scenario is
        affected (current production, expansion/upgrade, or a product branch);
        whether the named target actually depends on this node; which alternatives
        are already qualified and have usable capacity; whether switching and
        recovery exceed known buffers; and what evidence would falsify the view.

        Call ${go_tool.submit_node_assessment.id}; r42 binds the task workspace
        and declared node-assessment artifact_id.
        all claim_paths above, and the target node. risk_scope is global or
        branch; conclusion is independently confirmed, candidate, or not_proven.
        Keep unknowns and falsification_conditions explicit.
      PROMPT
      import_artifact = {
        supply_chain = {
          desc    = "Validated supply-chain map and selected node targets."
          sources = values(research.static.build_supply_chain.artifact)
        }
        baseline = {
          desc    = "Validated baseline claims for node assessment evidence."
          sources = values(research.static.primary_source_baseline.artifact)
        }
        track_claims = {
          desc    = "Validated graph-track claims for node assessment evidence."
          sources = flatten([for task in research.dynamic.graph_track.tasks : values(task.artifact)])
        }
      }
      tool_use = {
        submit_node_assessment = {
        tool_id   = go_tool.submit_node_assessment.id
        terminate = true
        input = {
          workspace_dir        = "${block_wd()}/${index}"
          artifact_id          = artifact("node_assessment").id
          _r42_artifact_path   = ""
          claim_paths = concat(
            [research.static.primary_source_baseline.artifact.claims.path],
            [for task in research.dynamic.graph_track.tasks : task.artifact.claims.path],
          )
          node_id   = target.node_id
          node_name = target.node_name
        }
        input_from_agent = {
          risk_scope = {
            desc = "Whether this assessment applies globally or only to a branch."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
          }
          branch = {
            desc = "The applicable product branch when risk_scope is branch."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
          }
          scenarios = {
            desc = "Scenarios selected from current_production, expansion_upgrade, and product_branch."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
          }
          actual_dependency = {
            desc = "Evidence-backed description of the target's actual dependency on this node."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
          }
          qualified_alternatives = {
            desc = "Known qualified alternatives and usable capacity."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
          }
          switching_vs_buffer = {
            desc = "Comparison of switching and recovery constraints with known buffers."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
          }
          conclusion = {
            desc = "Conclusion selected from confirmed, candidate, or not_proven."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
          }
          claim_ids = {
            desc = "Claim IDs supporting this node assessment."
            sources = flatten([values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
          }
          unknowns = {
            desc = "Material unknowns that prevent a stronger conclusion."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
          }
          falsification_conditions = {
            desc = "Evidence that would falsify or materially change this assessment."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
          }
        }
        }
      }
      disallowed_tools  = local.offline_disallowed_tools
      permission        = "approve_all"
      artifact = {
        node_assessment = {
        type      = "file"
        path      = "${block_wd()}/${index}/node-assessment.json"
		description = "Evidence-based continuity-risk assessment for the target node"
        required  = true
        non_empty = true
        }
      }
      retry = null
      collection_qc = {
        model_provider = model_provider.qc
      }
      qc = {
        criteria = {
          dependency = "Judge whether public evidence actually establishes the target's dependency rather than only an industry-wide possibility."
          alternatives = "Judge whether qualified alternatives, available capacity, switching constraints, and buffers are treated separately and honestly."
          conclusion = "Judge whether confirmed, candidate, or not_proven follows from the evidence for the stated scenario and branch. Not proven means insufficient public evidence, not no risk."
        }
        model_provider   = model_provider.qc
        model            = local.qc_model
        reasoning_effort = var.reasoning_effort
        max_qc_rounds    = var.max_qc_rounds
        disallowed_tools = local.semantic_qc_disallowed_tools
        permission       = "approve_all"
      }
    }
  ]
}

research "dynamic" "prioritize_companies" {
  tasks = [
    for index, target in jsondecode(research.static.build_supply_chain.result).company_mapping_targets : {
      model_provider   = model_provider.primary
      model            = var.model
      reasoning_effort = var.reasoning_effort
      system_prompt    = var.research_system_prompt
      prompt = <<-PROMPT
        Topic: ${var.topic}
        Evidence cutoff: ${var.as_of_date}
        Market: ${var.market}
        Selected company-mapping target JSON:
        ${jsonencode(target)}
        Validated supply-chain JSON:
        ${research.static.build_supply_chain.result}
        Validated existing claim JSON:
        ${research.static.primary_source_baseline.result}
        ${join("\n", [for item in research.dynamic.graph_track.tasks : item.result])}
        Task workspace: "${block_wd()}/${index}"
        ${local.source_tool_guidance}
        ${var.use_pplx ? format("Perplexity artifact_dir: %s", artifact("sources").path) : ""}

        During Collection, investigate whether any public company deserves more
        research because of this exact supplier-addressable node. Investigate at most
        ${var.max_candidates_per_chokepoint} companies. For each, distinguish an
        existing supplier, qualified alternative, exact-node capability match,
        related-product-only company, or unverified lead. Verify the exact legal
        entity and security. A capability_match means confirmed ability to supply
        this exact node, not proof of a named customer relationship. Save
        every retained source at a unique .md path under "${artifact("sources").path}"
        with r42_save_artifact,
        then submit a collection checkpoint. If no new evidence is needed,
        submit an empty collection checkpoint.

        During closed Research, do not search or fetch. Use the authorized artifact_id
        values supplied to this phase with r42_read_artifact.

        Register retained sources with ${go_tool.register_evidence_source.id}
        using the authorized artifact_id and workspace_dir above.
        Submit atomic relationship, capability, and economic-exposure cards with
        ${go_tool.submit_claim_cards.id}, using the registered source's same
        artifact_id for every direct card. If revision removes an earlier card,
        include its ID in remove_claim_ids in the same call; missing removal IDs
        are harmless. Different claims may share a source, artifact, and quote.
        Never claim that a related product proves adoption, qualification, market
        share, revenue, or profit impact.

        For each company, assess four separate economic-exposure dimensions:
        customer_validation, revenue_materiality, bottleneck_capture, and
        commercialization_timing. Every dimension has status,
        evidence_directness, and claim_ids. evidence_directness must be none,
        confirmed, reported, or inferred and must match the status of every
        referenced atomic claim card. Use unknown with evidence_directness none
        and empty claim_ids when the available evidence does not establish a
        dimension. Never fill an evidence gap with a related-product analogy.
        Commercialization timing is measured from the evidence cutoff;
        within_12_months and beyond_12_months describe expected supplier revenue
        timing, not merely the target product's launch date.

        Also record any useful quantitative or qualitative exposure_signals
        without forcing unlike scopes into one score. Each signal must name its
        scope (company, segment, modality, target_branch, or named_program),
        subject, metric, value, as_of, evidence_directness, and claim_ids. Omit
        signals that cannot be supported by an atomic claim; do not translate a
        segment or modality metric into named-program economics.

        During each Research pass, finalize the current staged card set exactly
        once with
        ${go_tool.finalize_claim_cards.id}, claims_path
        "${artifact("claims").path}",
        source_registry_path
        "${artifact("source_registry").path}",
        cutoff above, and allow_empty true. Exact duplicate cards are collapsed
        automatically. Do not call finalize repeatedly within the same pass
        merely to inspect its output. If Final QC returns this block to Research,
        revise the staged cards and finalize again in the later pass.

        Then call ${go_tool.submit_company_priorities.id}; r42 binds its
        declared company-priorities artifact_id,
        authoritative supply-chain path, selected target node ID, and claim_paths
        containing the baseline, all five track files, and this task's claims.json.

        research_priority is separate from role and evidence maturity. A means
        the exact-node relationship or capability is confirmed and a high-value
        unresolved question has an executable next_check; it does not require a
        named target customer. B means the exact-node link is plausible but its
        relationship, qualification, or benefit mechanism is incomplete. C is
        only an industry or related-product lead.
        do_not_research means the node or company link is too weak. These are
        research priorities, never investment ratings. An empty list is valid.
      PROMPT
      collection_tool_ids = local.pplx_tool_ids
      import_artifact = {
        supply_chain = {
          desc    = "Authoritative supply-chain map and selected company-mapping target."
          sources = values(research.static.build_supply_chain.artifact)
        }
        baseline = {
          desc    = "Validated baseline claims for company evidence."
          sources = values(research.static.primary_source_baseline.artifact)
        }
        track_claims = {
          desc    = "Validated graph-track claims for company evidence."
          sources = flatten([for task in research.dynamic.graph_track.tasks : values(task.artifact)])
        }
      }
      tool_use = {
        register_source = {
          tool_id = go_tool.register_evidence_source.id
          input = {
            workspace_dir = "${block_wd()}/${index}"
          }
        }
        submit_claims = {
          tool_id = go_tool.submit_claim_cards.id
          input = {
            workspace_dir = "${block_wd()}/${index}"
            claims_path   = artifact("claims").path
          }
          input_from_agent = {
            cards = {
              desc = "Atomic relationship, exact-node capability, and economic-exposure claim cards grounded in current registered sources and the selected supply-chain target."
              sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
            }
          }
        }
        finalize_claims = {
          tool_id = go_tool.finalize_claim_cards.id
          input = {
            workspace_dir        = "${block_wd()}/${index}"
            claims_path           = artifact("claims").path
            source_registry_path  = artifact("source_registry").path
            as_of_date            = var.as_of_date
            allow_empty           = true
          }
        }
        submit_company_priorities = {
          tool_id   = go_tool.submit_company_priorities.id
          terminate = true
          input = {
            workspace_dir        = "${block_wd()}/${index}"
            artifact_id          = artifact("company_priorities").id
            _r42_artifact_path   = ""
            supply_chain_path    = research.static.build_supply_chain.artifact.supply_chain.path
            target_node_id       = target.node_id
            claim_paths = concat(
              [research.static.primary_source_baseline.artifact.claims.path],
              [for task in research.dynamic.graph_track.tasks : task.artifact.claims.path],
              [artifact("claims").path],
            )
          }
          input_from_agent = {
          companies = {
            desc = "Companies mapped to the exact selected supply-chain node. For every public company provide exact legal company name, non-empty ticker, market, role (existing_supplier, qualified_alternative, capability_match, related_product_only, or unverified), research_priority, relationship_claim_ids, capability_claim_ids, why_research, largest_unknown, executable next_check, economic_exposure, and exposure_signals. Use relationship_claim_ids only for customer/supplier or qualification relationships; use capability_claim_ids for evidence that the company can supply the exact node without claiming a named customer. A requires confirmed evidence appropriate to the declared role but does not require a named target customer. Each economic dimension requires status, evidence_directness (none, confirmed, reported, or inferred), and claim_ids. customer_validation status: unknown, evaluation, qualified, ordered, delivering, or production_use. revenue_materiality status: unknown, exposure_unquantified, quantified_immaterial, or quantified_material. bottleneck_capture status: unknown, none, plausible, or demonstrated. commercialization_timing status: unknown, current, within_12_months, or beyond_12_months. Each optional exposure_signal has scope (company, segment, modality, target_branch, or named_program), subject, metric, value, as_of, evidence_directness, and claim_ids; preserve its actual scope and never promote a broad metric to a named program. The current task claim IDs returned earlier by submit_claim_cards are available in this session even though sources lists only authorized imported artifacts. Use validated upstream claims plus those current task IDs; use unknown/none/[] or an empty exposure_signals list when evidence is absent."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
            }
          conclusion = {
            desc = "The concise conclusion for this company-mapping target's priority list."
            sources = flatten([values(research.static.build_supply_chain.artifact), values(research.static.primary_source_baseline.artifact), [for task in research.dynamic.graph_track.tasks : values(task.artifact)]])
            }
          }
        }
      }
      tool_call_quota   = local.pplx_tool_call_quota
      disallowed_tools  = local.research_disallowed_tools
      permission        = "approve_all"
      artifact = {
        sources = {
          type        = "directory"
          path        = "${block_wd()}/${index}/artifacts/sources"
          description = "Company-specific source material for this supply-chain mapping target."
        }
        claims = {
          type      = "file"
          path      = "${block_wd()}/${index}/claims.json"
		  description = "Validated company relationship and exposure claim cards"
          required  = true
          non_empty = true
        }
        source_registry = {
          type      = "file"
          path      = "${block_wd()}/${index}/source-registry.json"
		  description = "Source metadata for company-prioritization evidence"
          required  = true
          non_empty = true
        }
        company_priorities = {
          type      = "file"
          path      = "${block_wd()}/${index}/company-priorities.json"
		  description = "Companies prioritized for further research against this mapping target"
          required  = true
          non_empty = true
        }
      }
      retry = null
      collection_qc = {
        model_provider = model_provider.qc
      }
      qc = {
        criteria = {
          company_gate = "Judge research_priority separately from role evidence. A requires confirmed exact-node relationship or capability plus a valuable executable next check; a named target customer is not required for capability_match. Industry relevance alone is C at most."
          relationship = "Judge whether relationship_claim_ids and capability_claim_ids distinguish exact-node capability from customer validation, orders, delivery, production use, and primary-supplier status without upgrading one into another."
          exposure_dimensions = "Judge each company's customer_validation, revenue_materiality, bottleneck_capture, and commercialization_timing separately. Each non-unknown status must be semantically entailed by its own claim_ids at the declared evidence_directness; do not let evidence for one dimension support another."
          exposure_signals = "Judge whether each exposure signal preserves its declared company, segment, modality, target_branch, or named_program scope and whether its metric, value, date, and directness are entailed by its claim IDs."
          economic_boundary = "Judge whether revenue, profit, order, capacity, and competitive significance remain unknown unless directly supported. A/B/C are follow-up priorities, not investment recommendations."
        }
        model_provider   = model_provider.qc
        model            = local.qc_model
        reasoning_effort = var.reasoning_effort
        max_qc_rounds    = var.max_qc_rounds
        disallowed_tools = local.semantic_qc_disallowed_tools
        permission       = "approve_all"
      }
    }
  ]
}

research "static" "synthesize" {
  model_provider   = model_provider.primary
  model            = local.high_impact_model
  reasoning_effort = var.reasoning_effort
  system_prompt = <<-PROMPT
    Write a concise, company-first research-priority report. Every substantive
    clause must cite one original atomic claim ID. Do not create report-level
    claims, scores, investment ratings, or recommendations. Do not research new
    facts. Preserve not-proven nodes, rejected companies, and unknowns. Merge
    records for the same legal entity and security across company-mapping tasks,
    while preserving every distinct mapped node and its evidence.
  PROMPT
  prompt = <<-PROMPT
    Topic: ${var.topic}
    Evidence cutoff: ${var.as_of_date}
    Validated scope and supply-chain JSON:
    ${research.static.brainstorm.result}
    ${research.static.build_supply_chain.result}
    Validated node assessments:
    ${join("\n", [for task in research.dynamic.assess_nodes.tasks : task.result])}
    Validated company priorities:
    ${join("\n", [for task in research.dynamic.prioritize_companies.tasks : task.result])}
    Validated baseline and track claims:
    ${research.static.primary_source_baseline.result}
    ${join("\n", [for item in research.dynamic.graph_track.tasks : item.result])}
    Finalized claim_paths JSON array:
    ${jsonencode(local.synthesis_claim_paths)}

    The `claims` fields in the baseline, track, and company-priority results are
    the complete atomic claim-card JSON available for report citations. Each
    company-priority result includes only the new claims produced by that task;
    baseline and track claims are listed once above.

    During Collection, do not acquire new evidence. The validated JSON above is
    complete for synthesis. Call r42_set_information_needs once with a single
    need whose stop condition is satisfied by the validated JSON, then submit
    an empty collection checkpoint with empty_reason "validated upstream JSON
    is the complete closed input" and mark that need stalled.

    During closed Research, use only the validated JSON above and write
    "${artifact("report").path}" in this order:
    1. companies worth further research, showing research priority separately
       from relationship or capability evidence; merge the same legal entity and
       security across tasks while preserving all mapped nodes, roles, all four
       economic-exposure dimensions, scoped exposure signals, strongest evidence,
       largest unknown, and next check;
    2. confirmed and candidate global or branch-specific risk nodes;
    3. separate views for current production, expansion/upgrade, and product
       branches;
    4. concise node assessments and falsification conditions;
    5. not-proven nodes and unresolved questions;
    6. the decision-relevant supply-chain map and scope limitations.

    A/B/C are research priorities, never investment ratings. Put
    [[claim:CLAIM-ID]] immediately after every substantive atomic clause. If one
    sentence contains several facts, split it or cite each clause separately.
    Do not cite internal artifact counts or methodology statements as external
    facts. Do not write a URL table or RPT IDs.

    Finish with ${go_tool.finalize_research_report.id}. Set report_path to exactly
    "${artifact("report").path}". Set claim_paths to the Finalized claim_paths JSON
    array above, unchanged. Every element is an absolute path to one finalized claims.json
    artifact; do not substitute directories, globs, Markdown files,
    artifact IDs, or guessed paths. The tool replaces claim markers with original
    URLs and appends the referenced evidence cards.
  PROMPT
  import_artifact "baseline" {
    desc    = "Validated baseline claims for final report citations."
    sources = values(research.static.primary_source_baseline.artifact)
  }
  import_artifact "scope_and_map" {
    desc    = "Validated scope and supply-chain map for final report context."
    sources = concat(values(research.static.brainstorm.artifact), values(research.static.build_supply_chain.artifact))
  }
  import_artifact "track_claims" {
    desc    = "Validated graph-track claims for final report citations."
    sources = flatten([for task in research.dynamic.graph_track.tasks : values(task.artifact)])
  }
  import_artifact "node_assessments" {
    desc    = "Validated node continuity assessments for final report conclusions."
    sources = flatten([for task in research.dynamic.assess_nodes.tasks : values(task.artifact)])
  }
  import_artifact "company_priorities" {
    desc    = "Validated company-priority claims and recommendations for the report."
    sources = flatten([for task in research.dynamic.prioritize_companies.tasks : values(task.artifact)])
  }
  tool_use "finalize_report" {
    tool_id   = go_tool.finalize_research_report.id
    terminate = true
    input = {
      report_path = artifact("report").path
      claim_paths = local.synthesis_claim_paths
    }
  }
  disallowed_tools  = local.offline_disallowed_tools
  permission        = "approve_all"

  collection_qc {
    criteria = {
      closed_input = "This is closed-input synthesis over already validated upstream JSON. The single information need's stop condition is satisfied without new sources; assess it sufficient. Do not request new sources or re-review upstream evidence coverage."
    }
    model_provider   = model_provider.qc
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    permission       = "approve_all"
  }

  artifact "report" {
    type      = "file"
    path      = "${block_wd()}/report.md"
	description = "Final decision-oriented chokepoint research report"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      decision_usefulness = "Judge whether the first page merges duplicate legal entities or securities across mapped nodes and gives a defensible research-priority list with priority separate from relationship/capability evidence, exact nodes, roles, scoped exposure signals, customer validation, revenue materiality, bottleneck capture, commercialization timing, largest unknown, and next check."
      entailment = "Judge whether each cited atomic claim semantically supports the adjacent report clause without concept, party, period, product-branch, or qualifier substitution. Treat marker, ID, path, URL, and quotation checks as authoritative."
      risk_scope = "Judge whether global versus branch scope and current production versus expansion/upgrade scenarios are separated from proof strength."
      restraint = "Reject investment recommendations, composite scores, false precision, and any company promotion based only on industry relevance or a related product."
      uncertainty = "Judge whether not-proven nodes, rejected companies, missing economic exposure, alternatives, buffers, and falsification conditions remain visible."
    }
    model_provider   = model_provider.qc
    model            = local.qc_model
    reasoning_effort = var.reasoning_effort
    max_qc_rounds    = var.max_qc_rounds
    disallowed_tools = local.semantic_qc_disallowed_tools
    permission       = "approve_all"
  }
}

output "report_path" {
  description = "Final company-first research-priority report with direct source URLs."
  value       = research.static.synthesize.artifact.report.path
}

output "scope_path" {
  description = "Machine-readable product boundary and coverage inventory."
  value       = research.static.brainstorm.artifact.scope.path
}

output "supply_chain_path" {
  description = "Machine-readable reference supply chain with assessment and company-mapping targets."
  value       = research.static.build_supply_chain.artifact.supply_chain.path
}

output "node_assessment_paths" {
  description = "Node assessments with independent risk scope and evidence conclusion."
  value       = [for task in research.dynamic.assess_nodes.tasks : task.artifact.node_assessment.path]
}

output "company_priority_paths" {
  description = "Company follow-up research priorities by company-mapping target."
  value       = [for task in research.dynamic.prioritize_companies.tasks : task.artifact.company_priorities.path]
}
