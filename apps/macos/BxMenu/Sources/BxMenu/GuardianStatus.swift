import Foundation

/// Guardian `/v1/status`、`/v1/up`、`/v1/down` 响应体里菜单实际用到的字段。
///
/// 刻意只声明用得上的几个:Guardian 的 Status 还有 core_pid、dns_service 等十余个
/// 字段,`Decodable` 默认忽略未声明的键,新增字段不会让菜单解码失败。
struct GuardianStatus: Decodable {
    let desired: String
    let phase: String
    let protectionState: String
    /// 成功时服务端 omitempty 掉这个键,故为可选——不是「有但为空」。
    let lastError: String?

    enum CodingKeys: String, CodingKey {
        case desired
        case phase
        case protectionState = "protection_state"
        case lastError = "last_error"
    }
}
