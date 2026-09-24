# 材料核对记录模板

任务/阶段：REPLACE。核对时间：待填。执行位置：Fedora。状态：未执行。

## 固定来源
写官方仓库/发行tag/commit/Chart名、应用版本和Chart版本；分开记录继承研究选择与本次实际获取结果。目标机不得在线补包。

## 文件与镜像
每个文件写类型、来源、版本、相对路径、真实SHA256、批准依据。
每个镜像写源ref、平台、index digest（存在时）、platform manifest digest、local ref、served digest/转换说明、layer完整性。
动态Pod镜像、init/hook、验证工具、SDK/wheel、Notebook/guest/模型/数据集等都要列。不存在字段填null并说明，不编造。

## 配置字段契约
逐行列“本版上游字段 → ANI配置 → 渲染值 → 正/负测试”。禁用依赖用实际渲染资源证明。

## 校验与限制
真实命令、退出码、样本输出与日志路径；未验证组合/硬件/私有材料阻塞。
通过SHA256SUMS只说明完整性，不能单独证明批准来源。
