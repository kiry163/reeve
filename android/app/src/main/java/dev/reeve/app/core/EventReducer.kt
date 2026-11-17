package dev.reeve.app.core

import org.json.JSONArray
import org.json.JSONObject

/**
 * 一条 SSE 事件。持久事件的 [seq] > 0；临时事件（流式文字等）为 0，不能推进续传位点。
 * [data] 通常是 JSONObject，`message.hidden` 是 JSONArray。
 */
data class WireEvent(val seq: Long, val kind: String, val time: String, val data: Any)

/**
 * 事件溯源的状态机：`(BotState, WireEvent) -> BotState`。
 *
 * 纯函数，不修改入参、不产生副作用。`BotSession` 负责传输，这里只负责按契约 §3 归并。
 */
object EventReducer {

    fun empty(): BotState = BotState()

    fun apply(state: BotState, ev: WireEvent): BotState {
        // 断线续传会从服务端补发已经消费过的事件；seq 不前进的一律丢弃。
        if (ev.seq > 0 && ev.seq <= state.lastSeq) return state
        val base = if (ev.seq > 0) state.copy(lastSeq = ev.seq) else state
        return reduce(base, ev)
    }

    private fun reduce(s: BotState, ev: WireEvent): BotState = when (ev.kind) {
        "replay.done" -> s.copy(
            ready = true,
            busy = ev.json().bool("busy"),
            streaming = "",
            thinking = "",
            toolLabel = "",
            scheduleRevision = s.scheduleRevision + 1,
        )
        "message" -> onMessage(s, ev)
        "message.hidden" -> hideMessages(s, ev.array().strings())
        "message.reaction" -> onReaction(s, ev.json())
        "card" -> {
            val card = Card.from(ev.json())
            val msgs = s.msgs.mapValues { (_, m) -> if (m.card?.id == card.id) m.copy(card = card) else m }
            s.copy(cards = s.cards + (card.id to card), msgs = msgs)
        }
        "secret.card" -> {
            val req = SecretReq.from(ev.json())
            val msgs = s.msgs.mapValues { (_, m) -> if (m.secret?.id == req.id) m.copy(secret = req) else m }
            s.copy(secrets = s.secrets + (req.id to req), msgs = msgs)
        }
        "command.result" -> onCommandResult(s, ev)
        "task.created" -> onTaskCreated(s, ev)
        "task.updated" -> onTaskUpdated(s, ev)
        "task.pausing" -> {
            val d = ev.json()
            val id = d.str("id")
            val t = s.tasks[id] ?: return s
            s.copy(tasks = s.tasks + (id to t.copy(pausing = true, stopReason = d.strOrNull("stop_reason") ?: t.stopReason)))
        }
        "task.prio" -> {
            val d = ev.json()
            val id = d.str("id")
            val t = s.tasks[id] ?: return s
            val prio = d.num("prio") ?: return s
            s.copy(tasks = s.tasks + (id to t.copy(prio = prio)))
        }
        "task.summary" -> {
            val d = ev.json()
            val id = d.str("id")
            val t = s.tasks[id] ?: return s
            val files = d.arr("files").strings()
            s.copy(tasks = s.tasks + (id to t.copy(
                summary = d.strOrNull("summary") ?: t.summary,
                files = files.ifEmpty { t.files },
                duration = d.num("duration_sec")?.let { it * 1000 } ?: t.duration,
            )))
        }
        "agent" -> applyAgent(s, ev)
        "chat.thinking" -> s.copy(thinking = s.thinking + ev.json().str("text"))
        "chat.delta" -> s.copy(streaming = s.streaming + ev.json().str("text"))
        "chat.tool" -> s.copy(toolLabel = ev.json().str("label"))
        "chat.status" -> {
            if (!s.ready) s.copy(busy = false)
            else {
                val busy = ev.json().bool("busy")
                if (busy) s.copy(busy = true) else s.copy(busy = false, streaming = "", thinking = "", toolLabel = "")
            }
        }
        "schedule.upserted", "schedule.deleted", "schedule.run" -> s.copy(scheduleRevision = s.scheduleRevision + 1)
        // 不改变界面状态的事件：env.set/env.unset、executors.*、task.note/notes.used/reported、
        // message.handled、approval.record（后几类本来就不推给客户端）。
        else -> s
    }

    // ---------- 消息 ----------

    private fun onMessage(s: BotState, ev: WireEvent): BotState {
        val d = ev.json()
        val role = d.str("role")
        if (role == "notice") return s // 系统通知只给聊天层看
        val id = d.str("id")
        if (id.isEmpty()) return s
        if (d.bool("hidden")) return hideMessages(s, listOf(id))

        val m = Msg.from(d)
        val cards = if (m.card != null) s.cards + (m.card.id to m.card) else s.cards
        val secrets = if (m.secret != null) s.secrets + (m.secret.id to m.secret) else s.secrets
        if (s.msgs.containsKey(id)) {
            return s.copy(msgs = s.msgs + (id to m), cards = cards, secrets = secrets)
        }

        // 聊天层的回应排在它这一轮刚派出的工作卡片前面，读起来更顺。
        val items = s.items.toMutableList()
        var at = items.size
        if (role == "assistant") {
            while (at > 0) {
                val prev = items[at - 1]
                val t = if (prev is Item.TaskItem) s.tasks[prev.id] else null
                if (t != null && t.steps.isEmpty() && t.created > m.created - 60_000) at-- else break
            }
        }
        items.add(at, Item.MessageItem(id))
        val live = if (role == "assistant" && (m.text.isNotEmpty() || !m.thinking.isNullOrEmpty())) {
            s.copy(streaming = "", thinking = "", toolLabel = "")
        } else s
        return live.copy(items = items, msgs = s.msgs + (id to m), cards = cards, secrets = secrets)
    }

    private fun hideMessages(s: BotState, ids: List<String>): BotState {
        if (ids.isEmpty()) return s
        val gone = ids.toSet()
        return s.copy(
            items = s.items.filterNot { it is Item.MessageItem && it.id in gone },
            msgs = s.msgs - gone,
        )
    }

    private fun onReaction(s: BotState, d: JSONObject): BotState {
        val m = s.msgs[d.str("id")] ?: return s
        val by = d.str("by")
        val keep = m.reactions.filter { it.by != by }.toMutableList() // 每一方一条，切换
        if (d.bool("on")) keep.add(Reaction(by, d.str("emoji")))
        return s.copy(msgs = s.msgs + (m.id to m.copy(reactions = keep)))
    }

    private fun onCommandResult(s: BotState, ev: WireEvent): BotState {
        val d = ev.json()
        val id = if (ev.seq > 0) "cmd_${ev.seq}" else "cmd_${Stamp.ms(ev.time).toLong()}"
        val m = Msg(
            id = id, role = "notice", text = d.str("text"),
            card = null, secret = null, tools = emptyList(), created = Stamp.ms(ev.time),
            thinking = null, thinkingMs = null, files = emptyList(), reactions = emptyList(),
            ok = d.optBoolean("ok", true),
        )
        val items = if (s.msgs.containsKey(id)) s.items else s.items + Item.NoticeItem(id)
        return s.copy(items = items, msgs = s.msgs + (id to m))
    }

    // ---------- 任务 ----------

    private fun onTaskCreated(s: BotState, ev: WireEvent): BotState {
        val d = ev.json()
        val id = d.str("id")
        if (id.isEmpty()) return s
        val created = Stamp.ms(d.strOrNull("created_at"))
        val prio = d.num("prio")?.takeIf { it != 0.0 } ?: (created * 1e6)
        val task = BotTask(
            id = id, title = d.str("title"), input = d.str("input"),
            status = TaskStatus.of(d.str("status")) ?: TaskStatus.queued,
            executor = d.str("executor"),
            stopReason = d.strOrNull("stop_reason")?.takeIf { it.isNotEmpty() },
            created = created, prio = prio, lastEvent = created, lastBeat = created,
        )
        val items = if (s.tasks.containsKey(id)) s.items else s.items + Item.TaskItem(id)
        return s.copy(items = items, tasks = s.tasks + (id to task))
    }

    private fun onTaskUpdated(s: BotState, ev: WireEvent): BotState {
        val d = ev.json()
        val id = d.str("id")
        val t = s.tasks[id] ?: return s
        var next = t

        val status = TaskStatus.of(d.strOrNull("status"))
        if (status != null) {
            next = next.copy(status = status)
            // 终态：后端「排队中取消 / 启动失败」只写 task.updated，不再发 agent 终态。
            if (status.terminal) next = next.copy(ended = Stamp.ms(ev.time))
            // queued/running 表示暂停请求已撤销或进程已恢复，别让界面停在「正在暂停」。
            if (status == TaskStatus.queued || status == TaskStatus.running) next = next.copy(pausing = false)
        }
        if (status == TaskStatus.queued) next = next.copy(stopReason = null)
        d.num("prio")?.takeIf { it != 0.0 }?.let { next = next.copy(prio = it) }
        d.strOrNull("error")?.takeIf { it.isNotEmpty() }?.let { next = next.copy(error = it) }
        if (d.bool("clear_stop_reason")) next = next.copy(stopReason = null, pausing = false)
        else d.strOrNull("stop_reason")?.takeIf { it.isNotEmpty() }?.let { next = next.copy(stopReason = it) }
        return s.copy(tasks = s.tasks + (id to next))
    }

    // ---------- agent 子事件 ----------

    private fun applyAgent(s: BotState, ev: WireEvent): BotState {
        val e = ev.json()
        val taskId = e.str("task_id")
        val type = e.str("type")
        val d = e.obj("data") ?: JSONObject()
        val at = Stamp.ms(e.strOrNull("time")?.takeIf { it.isNotEmpty() } ?: ev.time)

        when (type) {
            "approval.requested" -> {
                val id = d.str("id")
                if (id.isEmpty()) return s
                if (s.approvals.containsKey(id)) return s
                val approval = Approval(
                    id = id, taskId = taskId, toolName = d.str("tool_name"),
                    args = d.obj("args") ?: JSONObject(), risk = d.str("risk"), reason = d.str("reason"),
                    status = "pending", scope = null,
                    created = Stamp.ms(d.strOrNull("created_at")?.takeIf { it.isNotEmpty() } ?: e.strOrNull("time") ?: ev.time),
                )
                return s.copy(
                    items = s.items + Item.ApprovalItem(id),
                    approvals = s.approvals + (id to approval),
                    tasks = bumpAwaiting(s.tasks, taskId, at, +1),
                )
            }
            "approval.decided" -> {
                val id = d.strOrNull("approval_id")?.takeIf { it.isNotEmpty() } ?: d.str("id")
                val a = s.approvals[id] ?: return s
                if (a.status != "pending") return s
                val decided = a.copy(status = d.str("decision").ifEmpty { "expired" }, scope = d.strOrNull("scope"))
                return s.copy(
                    approvals = s.approvals + (id to decided),
                    tasks = bumpAwaiting(s.tasks, taskId, at, -1),
                )
            }
            "heartbeat" -> {
                val t = s.tasks[taskId] ?: return s
                return s.copy(tasks = s.tasks + (taskId to t.copy(lastBeat = at)))
            }
        }

        val t = s.tasks[taskId] ?: return s
        var next = t.copy(lastEvent = maxOf(t.lastEvent, at))
        when (type) {
            "task.started" -> next = next.copy(status = TaskStatus.running, stopReason = null, pausing = false, started = at, ended = null)
            "thinking.delta" -> next = next.copy(thinking = (next.thinking + d.str("text")).takeLast(600))
            "text.delta" -> next = next.copy(text = (next.text + d.str("text")).takeLast(4000), thinking = "")
            "tool.call" -> {
                val thought = if (next.executor.isEmpty()) next.text.trim() else ""
                val step = Step(d.str("call_id"), d.str("tool"), d.obj("args") ?: JSONObject(), thought, at = at)
                next = next.copy(
                    thinking = "",
                    text = if (next.executor.isEmpty()) "" else next.text,
                    steps = next.steps + step,
                )
            }
            "tool.result" -> {
                val steps = next.steps.toMutableList()
                val callId = d.str("call_id")
                val idx = steps.indexOfLast { it.id == callId && !it.done }.takeIf { it >= 0 } ?: (steps.size - 1)
                if (idx >= 0) {
                    val done = steps[idx].copy(
                        output = d.strOrNull("output"), error = d.bool("error"), denied = d.bool("denied"),
                        done = true, doneAt = at,
                    )
                    steps[idx] = done
                    val path = done.args.str("path")
                    var files = next.files
                    if ((done.tool == "write_file" || done.tool == "edit_file") && path.isNotEmpty() &&
                        !done.error && !done.denied && path !in files
                    ) files = files + path
                    next = next.copy(steps = steps, files = files)
                }
            }
            "task.paused" -> next = next.copy(
                activeMs = next.activeMs + (next.started?.let { at - it } ?: 0.0),
                status = TaskStatus.paused, pausing = false, text = "", thinking = "",
                started = null, steps = next.steps.map { it.copy(done = true) },
            )
            "task.completed", "task.failed", "task.cancelled" -> {
                val status = TaskStatus.of(d.strOrNull("status"))?.takeIf { it.terminal }
                    ?: TaskStatus.of(type.removePrefix("task.")) ?: TaskStatus.completed
                next = next.copy(
                    activeMs = next.activeMs + (next.started?.let { at - it } ?: 0.0),
                    status = status, output = d.strOrNull("output"), ended = at, started = null, pausing = false,
                    text = "", thinking = "",
                    error = d.strOrNull("error")?.takeIf { it.isNotEmpty() } ?: next.error,
                    steps = next.steps.map { it.copy(done = true) },
                )
            }
        }
        return s.copy(tasks = s.tasks + (taskId to next))
    }

    private fun bumpAwaiting(tasks: Map<String, BotTask>, id: String, at: Double, delta: Int): Map<String, BotTask> {
        val t = tasks[id] ?: return tasks
        return tasks + (id to t.copy(awaiting = maxOf(0, t.awaiting + delta), lastEvent = maxOf(t.lastEvent, at)))
    }
}

private fun WireEvent.json(): JSONObject = data as? JSONObject ?: JSONObject()
private fun WireEvent.array(): JSONArray = data as? JSONArray ?: JSONArray()
