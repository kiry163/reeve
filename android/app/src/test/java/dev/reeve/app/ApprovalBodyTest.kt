package dev.reeve.app

import dev.reeve.app.core.Approval
import dev.reeve.app.ui.approvalBody
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ApprovalBodyTest {

    private fun approval(args: JSONObject) = Approval(
        id = "a1", taskId = "chat_1", toolName = "start_task", args = args,
        risk = "exec", reason = "", status = "pending", scope = null, created = 0.0,
    )

    @Test
    fun startsWithExecutorAndTitleThenPreview() {
        val body = approvalBody(
            approval(
                JSONObject()
                    .put("executor", "omp")
                    .put("title", "升级依赖")
                    .put("task_hash", "abc")
                    .put("preview", "看看有没有需要升级的依赖"),
            ),
        )
        assertTrue(body.startsWith("执行器：omp · 升级依赖\n"))
        assertTrue(body.endsWith("看看有没有需要升级的依赖"))
    }

    @Test
    fun showsPreviewAloneWhenNoExecutorOrTitle() {
        assertEquals("只做这一件事", approvalBody(approval(JSONObject().put("preview", "只做这一件事"))))
    }

    @Test
    fun fallsBackToPrettyJsonWithoutPreview() {
        val body = approvalBody(approval(JSONObject().put("command", "ls -la")))
        assertTrue(body.contains("\"command\""))
        assertTrue(body.contains("ls -la"))
    }
}
