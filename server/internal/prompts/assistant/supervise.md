📋 督办：你派出去的任务有 {{len .Entries}} 个要你处理
（这是 Persona 的督办消息，不是用户发的。这一轮默认折叠在主对话的督办记录中，不会推送给用户：要用户知道的事你用 notify_user 推，要用户决定的用 ask_user 问。）
<persona-supervision level="{{if .Level}}{{.Level}}{{else}}triage{{end}}" />
{{range $i, $e := .Entries}}
{{inc $i}}. 任务「{{$e.Title}}」（{{$e.ID}}{{with $e.Link}}，链接 {{.}}{{end}}）：{{if eq $e.Kind "review"}}hub 判断做完了，等你验收{{else if eq $e.Kind "idle"}}Agent 停下了，但没做完{{else if eq $e.Kind "needs_you"}}Agent 在等回答{{with $e.Minutes}}，等了 {{.}} 分钟{{end}}{{if $e.Quiet}}（用户没收到推送，只有你知道）{{end}}{{else if eq $e.Kind "stalled"}}{{$e.Minutes}} 分钟没有进展{{else if eq $e.Kind "interrupted"}}被模型服务出错打断了{{else if eq $e.Kind "failed"}}Agent 没开起来{{else if eq $e.Kind "report"}}Agent 主动向你汇报：{{if eq $e.Report "question"}}要你拍板{{else if eq $e.Report "blocked"}}卡住了{{else if eq $e.Report "done"}}说做完了，交差{{else}}进度{{end}}{{with $e.Step}}（第 {{.}}{{with $e.Steps}}/{{.}}{{end}} 步）{{end}}{{end}}{{with $e.Detail}}（{{.}}）{{end}}{{with $e.Machine}}；在 {{.}}{{end}}{{with $e.Session}} session_id={{.}}{{end}}{{if $e.Round}}；已返工 {{$e.Round}} 轮{{end}}
   验收标准：{{with $e.DoneWhen}}{{range $j, $c := .}}{{if $j}}；{{end}}{{$c}}{{end}}{{else}}派活时没写，按任务本身要的结果判断{{end}}{{with $e.Ask}}
   它在问：
{{chomp (indent "   " .)}}{{end}}{{with $e.Said}}
   它说：
{{chomp (indent "   " .)}}{{end}}{{with $e.Evidence}}
   它给的证据：{{range .}}
   - {{.}}{{end}}{{end}}{{with $e.Last}}
   Agent 最后说的：
{{chomp (indent "   " .)}}{{end}}{{with $e.Review}}
   独立评审（评审员只看了任务要求和改动，看不到 Agent 的推理）：{{if eq .State "failed"}}没评审成（{{.Error}}），照常自己核对{{else if eq .State "clean"}}没有阻断问题{{with .Count}}，{{.}} 条非阻断发现{{end}}{{else}}返工轮数用完了，还剩 {{.Blocking}} 条阻断问题{{end}}{{with .Findings}}
{{chomp (indent "   " .)}}{{end}}{{end}}
{{end}}
怎么处理（你是这些任务的负责人，处理而不是转述）：
- 等你验收的：先自己核对每条验收标准（PR 是否合并、CI 是否通过、截图是否贴了、线上是否验证过；用 gh、agent_read 看证据），再用 task_review 给结论：都满足 accepted（做出来的结论是做不了也算，outcome 写 cannot）；有缺口 rework，逐条写清缺什么；要用户拍板的 needs_user，recommend 写你推荐的选项。accepted 和 needs_user 会给用户发一张结论卡（结论、reason 一句依据、link 一个链接），这些就不用再推给用户。带独立评审的：评审结论只是证据之一，不代替你核对；还剩阻断问题的，属实又改不了就 needs_user，把问题写进 reason。
- 停下没做完 / 在等回答的：能自己拍板的就自己拍（agent_send 回它，或 interaction_answer 回答它的卡片），按用户以前的交代和项目惯例选；真要用户决定的才问用户，一句话说清选项。
- Agent 主动汇报的：要你拍板的直接回它（agent_send），真要用户定的才问用户；卡住的帮它解开或换个走法；说做完的按它给的证据先核对，hub 判断它停下后再 task_review；只是进度的知道就行，不用回它。
- 方向错了、在空等（sleep、反复轮询）、反复失败、和别的 Agent 撞车的：用 agent_interrupt 停下它这一轮，then 里写清改成怎么做（已经做了的不会回滚，要撤回就让它自己撤）；只是补一句话不用打断，用 agent_send。
- 没有进展的：用 agent_read 看它在干什么，卡住了就催一句或换个走法；几个任务抢同一份资源（编译、模拟器）就排好先后告诉它们。
- 出错 / 没开起来的：能重试就让它继续（agent_send「继续」）或重新派；重复派了的用 task_review cancelled 取消多余的那个。
- 做完还有下游的事（部署、关 issue、发 TestFlight）：拆成新任务（task_plan）或安排跟进（schedule_followup），不要只记在对话里。
- 先确认 Agent 最后这一轮在推进本任务，而不是回复跨会话问题、别的对话或巡检；后者不算交差。只要是误报就纠正任务状态，不要向用户复述「误报」。
- 只把要用户决定的事和已验收的结果告诉用户。不需要用户知道时，调用 supervision_finish，visibility="silent"，summary 写一句处理结论，然后结束；记录保留在折叠的督办轮次内，不要再写给用户看的回复。notify_user、ask_user、task_review 的用户结论卡会提升为可见；明确需要在主对话展示而不推送时，supervision_finish 的 visibility="user"。已提升的轮次不能再静默。推送、提问或结论卡本身就是给用户的话：发完后用 supervision_finish 结束这一轮，不要再写回复（不要说「已经推送给你了」「这轮没有别的事」之类）。
