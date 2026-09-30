# ANI-system 快速部署进度（2026-09-30）

独立任务来自 origin/main 78d8418307b6156954feb10aee8d6972d2ff1bbd，执行边界及唯一详细结果见 [WORK-RESULTS](execution/plans/ani-system-fast-r2/WORK-RESULTS.md)。

已按用户关机要求安全暂停，没有本任务后台进程或持有实验锁。P1 输入完整性校验通过（24 镜像、3 Chart），尚未导入 Harbor。默认关闭开关、一个首装尾部 role 和同一分步脚本已提交；局部 Python 24 项、Go 3 项及新增 SQL 边界 3 项检查通过。72 份 SQL 已准备但未执行。源码提交 9bb9d583481a0b00a284056ecd114bfb972d2bff 已同步到 Fedora 专属 detached 执行树；私有种子和访问材料保留，恢复时复用。草稿 [PR #4](https://github.com/zhangzhe-ctrl/ani-installer/pull/4) 已创建，未合并。既有实验协调入口仍待明确，尚未执行集群写入、初始化 SQL 或完整 gate/build；人工登录和裸机首装 not_run。详细 SHA、rc、首错和恢复边界统一保存在 WORK-RESULTS。

用户已恢复 Goal，确认 .10 无人争用；复用其既有安装锁。P1 Harbor 私有项目导入及 24 个实际 digest 回读完成（rc=0），新应用锁真实更新，原包不变。应用私有配置、S3 专用权限、专用库和分步应用部署继续执行；此刻应用启动与入口仍未通过，不提前记录完成。

P2 已准备复用种子的私有配置，专用 S3 用户与 ani-fast-* 权限策略执行成功。P3 专用库初始化修复 stdin 和一条冗余角色 membership 语句后完成：真实账本 72 条，首管关联 platform-admin，原 ani owner=ani_app 保持不变。核心服务及网关正在分阶段启动；完整 gate/build 和人工登录仍待执行。逐轮真实 rc/首错见 WORK-RESULTS。

P3～P5 现场部署与必要检查已通过：专用库增加一条限定 refresh-token RLS 修复后共 73 条迁移，真实密码登录返回 access/refresh token，匿名受保护 API 401/登录后 200；22 个应用及业务 Envoy Deployment Ready、两个 HTTPS Gateway Programmed。Console/BOSS/API/Session 健康入口 200、专用 S3 PUT/HEAD 200；crds/init/wait 分步重试均 rc=0，无重新生成凭据。源码冻结 df306ee4e99e54cde281a09a02a3e2e569a135b7，28 项 Python 局部检查通过，Fedora 正在一次 build-code.sh（内含完整 gate）。[交付说明](execution/plans/ani-system-fast-r2/DELIVERY.md) 列明入口、重试、材料与凭据路径；人工浏览器登录、真实模型业务和裸机首装未执行。

收尾源码冻结更新为 2cbed2f392aa5c6184f2d2177c14b54337ab4f6b：修复既有 gate 测试对应用上下文的白名单假设并核对实际开关/路径，保留全部检查。Fedora 完整 build-code.sh（含 gate）rc=0，源码指纹在测试/构建/制包期间不变。应用包/代码包归档及解包校验通过，最终交付脚本 wait rc=0、DB 账本仍为 73。本机交付目录 /home/chabking/ani-installer-delivery/ani-system-fast-20260930，归档摘要和重试命令见 DELIVERY/WORK-RESULTS；纯结果文档不重编包。人工浏览器登录、真实模型业务与裸机首装仍未执行，PR #4 未合并，GitHub 当前 CI 单独查看 PR head。
