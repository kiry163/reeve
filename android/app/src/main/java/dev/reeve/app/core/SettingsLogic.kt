package dev.reeve.app.core

import org.json.JSONArray
import org.json.JSONObject

// 设置页的纯数据与纯函数：环境变量表格、执行器草稿、探测三态。
// 放在 core 里是为了能在 JVM 单测里直接覆盖，不依赖 Compose 与网络。

// ---------- 环境变量 ----------

/** 环境变量表格的一行。名称可以暂时为空（新加的行还没填），保存时跳过。 */
data class EnvRow(val name: String, val value: String)

/**
 * 表格 → PUT `{"vars":{…}}` 的 map。
 * - 名称去首尾空白，空名称跳过；
 * - 重名以最后一行为准（用户删掉旧行再写同名的常见操作）；
 * - 删除行 = 表格里没有这个名称，整体替换时自然被删掉。
 */
fun envVars(rows: List<EnvRow>): Map<String, String> {
    val out = LinkedHashMap<String, String>()
    for (row in rows) {
        val name = row.name.trim()
        if (name.isEmpty()) continue
        out[name] = row.value
    }
    return out
}

/** GET 返回的 map → 稳定排序的表格（按名称），避免每次打开页面行序跳动。 */
fun envRows(vars: Map<String, String>): List<EnvRow> =
    vars.entries.sortedBy { it.key }.map { EnvRow(it.key, it.value) }

/**
 * 表格是否改过。切换目标时先清空 rows/saved，因此旧目标的残留数据不会把新目标误判为
 * 「有未保存修改」，也就不会让用户把别人的整表 PUT 到错误目标。
 */
fun envDirty(rows: List<EnvRow>, saved: List<EnvRow>): Boolean = envVars(rows) != envVars(saved)

/** 只有加载成功过且表格改过才允许保存；加载失败时保持空表，保存禁用。 */
fun canSaveEnv(loaded: Boolean, rows: List<EnvRow>, saved: List<EnvRow>): Boolean =
    loaded && envDirty(rows, saved)

/** PUT /api/env 的请求体：整层变量表（body 里没有的键会被服务端删除）。 */
fun envBody(vars: Map<String, String>): JSONObject = JSONObject().put("vars", JSONObject(vars))

// ---------- 执行器 ----------

/** 一个外部 CLI 的选项，对应 `clis[id]` 的 `cli_model` / `cli_auth` / `cli_channel`。 */
data class CliOptions(
    val model: String = "",
    val auth: String = "",
    val channel: String = "",
) {
    /** 按后端校验规则整理：pi 固定 channel；其它 CLI 空认证按订阅处理。 */
    fun normalized(kind: String): CliOptions {
        val m = model.trim()
        val ch = channel.trim()
        return if (kind == "pi") {
            CliOptions(model = m, auth = "channel", channel = ch)
        } else {
            val a = auth.trim().ifEmpty { "subscription" }
            CliOptions(model = m, auth = a, channel = if (a == "channel") ch else "")
        }
    }

    fun json(): JSONObject = JSONObject()
        .put("cli_model", model)
        .put("cli_auth", auth)
        .put("cli_channel", channel)

    companion object {
        fun from(j: JSONObject?): CliOptions = CliOptions(
            model = j.str("cli_model"), auth = j.str("cli_auth"), channel = j.str("cli_channel"),
        )
    }
}

/** GET/PUT B/executors 的形状。白名单语义是集合，但保留服务端给的有序数组。 */
data class ExecutorsConfig(
    val allowed: List<String> = listOf("builtin"),
    val default: String = "builtin",
    val clis: Map<String, CliOptions> = emptyMap(),
) {
    /** 脏检查签名：白名单按集合比较（顺序不算改动），CLI 选项按键名排序。 */
    fun signature(): String {
        val ids = allowed.filter { it.isNotBlank() }.distinct().sorted()
        val opts = clis.keys.sorted().joinToString(";") { id ->
            val o = clis[id] ?: CliOptions()
            "$id=${o.model}\u0001${o.auth}\u0001${o.channel}"
        }
        return "$ids|$default|$opts"
    }

    fun payload(): JSONObject = executorPayload(allowed, default, clis)

    companion object {
        fun from(j: JSONObject): ExecutorsConfig {
            val ids = LinkedHashSet<String>()
            ids.add("builtin")
            j.arr("allowed").strings().forEach { id -> if (id.isNotBlank()) ids.add(id) }
            val clis = LinkedHashMap<String, CliOptions>()
            val raw = j.obj("clis")
            raw?.keys()?.forEach { id -> clis[id] = CliOptions.from(raw.obj(id)) }
            val def = j.str("default").takeIf { it in ids } ?: "builtin"
            return ExecutorsConfig(ids.toList(), def, clis)
        }
    }
}

/**
 * 执行器草稿 → PUT body。
 * - `builtin` 恒在白名单第一位，无法取消；
 * - 默认值必须落在白名单里，否则回退 builtin；
 * - 取消勾选的 CLI 仍然保留 `clis` 条目（重新勾选不用重填），所以这里包含全部外部项。
 */
fun executorPayload(allowed: List<String>, default: String, clis: Map<String, CliOptions>): JSONObject {
    val ids = LinkedHashSet<String>()
    ids.add("builtin")
    for (id in allowed) {
        val clean = id.trim()
        if (clean.isNotEmpty() && clean != "builtin") ids.add(clean)
    }
    val def = default.trim().takeIf { it in ids } ?: "builtin"
    val out = JSONObject()
    for ((id, opts) in clis) {
        val clean = id.trim()
        if (clean.isEmpty() || clean == "builtin") continue
        out.put(clean, opts.normalized(clean).json())
    }
    return JSONObject()
        .put("allowed", JSONArray(ids.toList()))
        .put("default", def)
        .put("clis", out)
}

/** 探测三态：`probed=false` 时没有结论，不能当成不可用。 */
enum class CliAvailability { Unknown, Available, Unavailable }

fun cliAvailability(probed: Boolean, available: Boolean): CliAvailability = when {
    !probed -> CliAvailability.Unknown
    available -> CliAvailability.Available
    else -> CliAvailability.Unavailable
}

/** 已勾选的项始终可以取消；只有「探测过且明确不可用」才禁止新勾选。 */
fun cliSelectable(checked: Boolean, probed: Boolean, available: Boolean): Boolean =
    checked || cliAvailability(probed, available) != CliAvailability.Unavailable

// ---------- 技能 ----------

data class SkillInfo(val name: String, val description: String, val whenToUse: String, val path: String) {
    companion object {
        fun from(j: JSONObject) = SkillInfo(
            name = j.str("name"), description = j.str("description"),
            whenToUse = j.str("when_to_use"), path = j.str("path"),
        )
    }
}

/** GET B/skills：只读的技能索引与加载失败的错误。 */
data class SkillsIndex(val skills: List<SkillInfo> = emptyList(), val errors: List<String> = emptyList()) {
    companion object {
        fun from(j: JSONObject) = SkillsIndex(
            skills = j.arr("skills").objects().map(SkillInfo::from),
            errors = j.arr("errors").strings(),
        )
    }
}
