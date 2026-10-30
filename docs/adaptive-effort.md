# Adaptive reasoning effort for a coding agent

## Abstract
codeforge chooses a reasoning effort (low, medium or high) for each task before the agent starts. A small, cheap model scores the task on a fixed rubric, and a deterministic rule turns the scores into a level. High is a hard ceiling. On a 20-task evaluation set with 7 deliberate traps, the classifier scores 17/20 and gets every trap right. All three misses err on the side of effort the rubric author did not expect, not on the side of under-thinking safety-critical work.

## 1. Problem
Reasoning models let the caller trade cost and latency for answer quality through an effort setting. One fixed level is wrong most of the time: high wastes tokens and minutes on a typo, low under-thinks a race condition or a production change. Our first attempt used keywords and ticket IDs. Those are labels, not difficulty: a ticket can be a one-word copy change, and the word "auth" appears in trivial edits.

## 2. Prior work
- **Compute-optimal test-time scaling.** Snell et al. show that the best use of inference compute depends on prompt difficulty, and that allocating it per prompt beats a fixed budget by more than 4x in efficiency [1].
- **Difficulty-aware routing.** Hybrid LLM routes each query to a small or large model from predicted difficulty with a tunable quality target [2]. RouteLLM trains routers from preference data and cuts cost by more than 2x without losing quality [3]. FrugalGPT cascades cheap models before expensive ones [4].
- **Budget-aware reasoning.** TALE estimates a token budget per question and shows chain-of-thought often spends far more than needed [5].
- **LLM as a judge with a rubric.** Scoring against explicit criteria, with structured output, is more consistent than free-form judgement [6].

codeforge applies the routing idea to one model's effort dial instead of choosing between models, and replaces a trained router with a rubric, because we have no labelled preference data for our own tasks.

## 3. Method
1. **Signals.** The task text, plus cheap repository facts: files changed against `HEAD`, diff size, branch name. The branch name is passed only so the judge can be told to ignore it.
2. **Judge panel.** Two fast models from different families (`copilot/claude-haiku-5.5` and `copilot/gpt-6-luna` by default) each run one headless turn in parallel, with no profile, no repo map and low effort. A panel of diverse small judges agrees with human labels better than one large judge and costs less, because the judges' biases differ [7]. Each scores five dimensions from 0 to 2:
   - *ambiguity:* how open the goal is
   - *blast:* how many files, packages or services change
   - *risk:* whether the change itself alters security logic, data, production infra, concurrency or money
   - *novelty:* new design versus following an existing pattern
   - *verify:* how hard the result is to prove correct
   The rubric scores the effect of the change, not the domain it sits in, and states that ticket IDs carry no signal. Task text is wrapped in `<task>` and marked as data, to blunt prompt injection.
3. **Merge.** Risk takes the highest score any judge gave, so one judge spotting a production or security change is enough. The other four dimensions take the rounded mean. If one judge fails, the rest decide.
4. **Decision rule** (code, not model):
   - high if risk = 2, ambiguity = 2, total ≥ 7, or verify = 2 with total ≥ 5
   - low if total ≤ 2 and neither risk nor ambiguity is 2
   - medium otherwise
   Keeping the rule outside the model makes the mapping auditable, testable and stable across judge models.
5. **Fail safe.** If every judge fails, a timeout, a malformed answer or an out-of-range score returns medium, never high or low.
6. **Ceiling.** An explicit `--effort` from the user wins, but anything above high is rejected.

## 4. Evaluation
`effort/eval_test.go` (build tag `eval`) runs 20 tasks against the live judge. Seven are traps: a typo in an auth file (low), a one-line production replica change (high), a trivial ticket (low), "make it faster" (high), PR text (low), a pattern-following field addition (medium), a flaky race (high). Any trap miss fails the run. The pass bar is 85%.

| Version | Accuracy | Traps |
|---|---|---|
| Keywords + ticket IDs | not measured, misroutes traps by design | fails 3+ |
| Rubric v1 | 15/20 | 1 miss |
| Rubric v2 (effect-not-domain, location is not ambiguity, low allows one point of risk) | 17/20 | 0 misses |
| Rubric v2, two-judge panel (Claude + OpenAI) | 18/20 | 0 misses |

Remaining misses with the panel: a JWT library bump and a `--dry-run` flag on a destructive command go to high (expected medium). The single-judge miss on a package move is gone. The first two over-spend on genuinely sensitive code, which is the safe direction.

## 5. Limitations
- Effort is chosen once at launch. A session that grows harder needs `/effort`.
- 20 tasks is a small set; it guards against regressions, it does not prove calibration. Add real tasks from your history as they come.
- The panel costs two cheap calls in parallel (about 2-8 s). Tasks typed inside the TUI after launch are not re-classified.

## References
[1] C. Snell, J. Lee, K. Xu, A. Kumar. *Scaling LLM Test-Time Compute Optimally can be More Effective than Scaling Model Parameters.* arXiv:2408.03314, 2024. https://arxiv.org/abs/2408.03314
[2] D. Ding et al. *Hybrid LLM: Cost-Efficient and Quality-Aware Query Routing.* ICLR 2024, arXiv:2404.14618. https://arxiv.org/abs/2404.14618
[3] I. Ong et al. *RouteLLM: Learning to Route LLMs with Preference Data.* arXiv:2406.18665, 2024. https://arxiv.org/abs/2406.18665
[4] L. Chen, M. Zaharia, J. Zou. *FrugalGPT: How to Use Large Language Models While Reducing Cost and Improving Performance.* arXiv:2305.05176, 2023. https://arxiv.org/abs/2305.05176
[5] T. Han et al. *Token-Budget-Aware LLM Reasoning.* arXiv:2412.18547, 2024. https://arxiv.org/abs/2412.18547
[6] L. Zheng et al. *Judging LLM-as-a-Judge with MT-Bench and Chatbot Arena.* NeurIPS 2023, arXiv:2306.05685. https://arxiv.org/abs/2306.05685
[7] P. Verga et al. *Replacing Judges with Juries: Evaluating LLM Generations with a Panel of Diverse Models.* arXiv:2404.18796, 2024. https://arxiv.org/abs/2404.18796
