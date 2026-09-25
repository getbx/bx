package install

// CoreOutlivesGuardianEnv 由 darwin 上 Guardian 的 plist 与 AbandonProcessGroup **同一处**写出,
// 于是它在 Guardian 进程环境里出现,就证明**加载着的**那个任务带着那个键 —— 盘上的
// plist 可能已经换了而任务没重载,读文件证明不了这件事。Guardian 据此判断「我退出
// 时 Core 会不会跟着死」:只有答案是「不会」时,升级提交之后它才退出让 launchd 以
// 新二进制重启自己(D2)。用环境变量而不是命令行参数:回滚到旧二进制时,旧版不认识
// 的参数会让 Guardian 起不来,而多一个环境变量它根本看不见。
const CoreOutlivesGuardianEnv = "BX_CORE_OUTLIVES_GUARDIAN"
