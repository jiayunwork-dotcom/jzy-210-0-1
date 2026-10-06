# 现场动平衡后端服务

风机/泵/电机现场动平衡（影响系数法）的后端服务：记录每台机器的平衡过程、
完成矢量计算、保存并复用历史影响系数。Go 1.23 + Echo + MySQL 8.0，事件溯源。

## 启动

```bash
docker compose up --build
# API: http://localhost:8080/api/v1
```

无需数据库的本地试用（数据仅存内存）：

```bash
BACKEND=memory HTTP_ADDR=:8080 go run ./cmd/server
```

## 包结构

| 包 | 职责 |
|---|---|
| `internal/vec` | 幅值/相位矢量（复数表示）、每台机器的相位口径换算 |
| `internal/balance` | 复矩阵、影响系数估计、方阵求解、加权最小二乘、模型预测 |
| `internal/split` | 连续配重矢量向等角度配重孔的两孔拆分（合成矢量严格不变） |
| `internal/history` | 历史影响系数的融合、复用、失效判定 |
| `internal/store` | 领域模型、**事件存储与重放**、由事件序列推导全部计算结果；内存与 MySQL 两套实现共用同一重放引擎 |
| `internal/api` | Echo HTTP 接口 |

## 相位口径（vec 包）

每台机器的配置保存两样东西：

- `reference_deg`：机器的 0°（键相标记位置）在服务**内部坐标系**中的角度；
- `phase_direction`：角度沿转向（`with_rotation`）还是逆转向
  （`against_rotation`，内部统一口径）递增。

换算关系：

- 逆转向口径：`内部角 = reference + 外部角`
- 沿转向口径：`内部角 = reference − 外部角`（镜像，影响系数相位取反）

振动读数入库时即换算为内部口径；配重建议、影响系数输出时再换算回机器口径。
键相参考偏移对"重量→振动"的传递关系自动抵消，仅角度增长方向影响系数相位。

## 线性模型与求解（balance 包）

在每个转速下（各转速独立求解）：

```
v_run[p] = v0[p] + Σ_q H[p,q] · w_run[q]
```

`v` 为测点振动矢量（μm），`w` 为校正面上的附加重量矢量（g，内部角度），
`H` 为影响系数矩阵（μm/g，测点×校正面）。所有运行（不只逐面试重）一并进入
按测点的复数最小二乘拟合；标准的"每面一次试重"只是设计矩阵对角的特例。

校正配重求解 `min_w ‖W(Hw + v0)‖₂`。

- **方阵**（测点数=校正面数）：高斯消元带部分主元精确求解，残余为零
  （浮点误差量级）。
- **超定**（测点多于校正面）：采用**按测点加权的复数最小二乘**
  （法方程 `HᴴW²H w = −HᴴW²v0`）。理由：传感器噪声近似零均值时这是极大
  似然解；它最小化加权残余幅值平方和。数学上可保证：残余 2-范数不大于
  "只对任意单个测点做平衡"的残余——单测点解只是最小二乘的一个可行 `w`，
  最小二乘的最优值不可能更差。测点权重 `point_weights` 默认为 1，噪声大的
  轴承座可给小权重。
- 条件数：估计返回法方程矩阵的倒数条件数估计（`rcond`），低于 `1e-4`
  判为病态（典型原因：两面试重在几乎相同的角度方向），拒绝出结果。
- 试重效应判据：试重引起的振动变化 RMS 必须 ≥ 3 × 噪声底（噪声底取
  `max(2 μm, 2%·原始振动RMS)`），否则影响系数被噪声淹没（现场最常见原因：
  相位录反 180° 或口径用反），拒收并指出字段。

自洽关系（有测试覆盖）：

- 方阵情形，将算出的配重代回模型，预测残余为零；
- 任意配重 `w`：`Predict(H, v0, w)` 后再按该预测反求，得回原 `w`；
- 80∠30° + 20g@0° 试重 → 89.44∠56.57° ⇒ H = 2 μm/g∠120°，
  配重 40 g∠90°（核对用单面例子）。

## 配重孔拆分（split 包）

校正面只有 `N ≥ 3` 个等角度孔时，把目标矢量用目标角相邻两孔的单位矢量
做正弦定理解析分解：

```
m_k = M·sin((1−δ)·step)/sin(step)
m_{k+1} = M·sin(δ·step)/sin(step)
```

`δ` 为目标角在两孔间的位置比例。`N ≥ 3` 保证两个分量均非负（不需要去重）；
两孔矢量和与目标矢量恒等（误差 < 1e-12，有遍历全部角度的测试）。孔位、
角度均按机器口径表达；`hole_layout` 中该面为 `null` 表示可任意角度配重，
直接给出整块重量。孔数 < 3 在机器配置时拒收。

## 历史系数管理（history 包）

系数按**机器 + 转速**保存。融合策略（机器配置项，默认第三种）：

1. `latest`：只用最近一次成功试重作业的估计；
2. `sliding_weighted`：最近 5 次估计，权重 1, 1/2, 1/3, …；
3. `uncertainty_weighted`（默认）：按每次估计的相对标准误 `u` 做
   **逆方差融合**，权重 `1/u²`。`u` 由该次最小二乘拟合残差相对激励响应
   的大小估计（下限 5%）——干净、激励充分的作业天然主导，数据差的作业权重
   自动变小，无需人工判断该信谁。仅融合"已被本作业验证运行证实"的估计；
   在有任何已验证估计之前先使用未验证估计，保证第一次作业也能被复用。

复用与失效：

- 新作业可标记 `use_history`。创建作业时把当时选中的系数矩阵**快照进作业
  事件**（复用作业的派生结果因此仍是自身事件的纯函数），随后可直接给出
  配重建议，免做试重运行。
- 复用作业的验证运行按"模型预测振动 vs 实测振动"的失配 RMS 判定：
  **失配 > 0.5 × 原始振动 RMS** 即判定历史系数不可信，立即将该转速系数
  置为不可用，下一个复用作业被拒绝（HTTP 409），必须重新试重。阈值含义：
  连要消除振动量的一半都预测不准，继续信任有加反配重的风险。仅残余降低
  不足（但预测准确）不判失效——那可能是该面本来就平衡不下来。
- 一次新的成功试重作业立即恢复该转速系数（并丢弃已被证伪的旧估计池）。

## 事件存储与重放（store 包）

一次作业的一切均为**只追加事件**：`job_created`（含机器配置快照、复用
作业的历史系数快照）、`run_recorded`、`run_corrected`。作业的影响系数、
配重建议、对机器历史的贡献全部由重放事件序列推导，不存任何派生数据。

- **更正**录错的读数：追加 `run_corrected` 事件（读数/配重的完整重述），
  旧值永不覆盖。重放结果与"一开始就录对"**完全一致**（有等价性测试：
  逐复数分量比较影响系数、配重、残余与历史贡献）。
- **并发提交**：两名技术员同一作业各提交一条运行，两条都保留；排序规则
  为"运行时间优先、提交序号（seq）决胜"，更正事件排在目标运行之后。
  相同运行时间允许并存；更正同一时刻的多条运行时须带 `target_seq` 消歧，
  否则返回 409。MySQL 侧用 `SELECT ... FOR UPDATE` 锁住作业事件序列
  （InnoDB 唯一键 `(job_id, seq)` 兜底）。
- MySQL 三张表：`machines`、`jobs`、`job_events`（payload 为 JSON）。

## 拒收规则（返回 422，逐字段列出）

- 幅值为负；相位不在 `[0,360)`；
- 校正面数不在 1–2、测点数不在 1–4；
- 试重为零；试重运行相对原始运行变化 < 3 倍噪声底（给出现算 SNR）；
- 配重孔数 < 3；
- 未知转速/测点、同一(转速,测点)重复读数、同一面重复重量条目等。

## HTTP 接口

```
GET  /api/v1/health
POST /api/v1/machines                       创建机器（含口径/孔位/融合策略）
GET  /api/v1/machines
GET  /api/v1/machines/:id
GET  /api/v1/machines/:id/history           每转速融合后的影响系数与可信度
POST /api/v1/machines/:id/jobs              {operator, use_history}
GET  /api/v1/machines/:id/jobs
GET  /api/v1/jobs/:id                        作业 + 运行 + 每转速完整计算结果
POST /api/v1/jobs/:id/runs                   提交一次运行
POST /api/v1/jobs/:id/corrections            更正某次运行（target_run_time[,target_seq]）
GET  /api/v1/jobs/:id/events                 原始事件流
```

角度一律机器外部口径；`weights` 描述当时转子上的**全部**附加重量。

### 单面核对例子

```bash
curl -s localhost:8080/api/v1/machines -H 'Content-Type: application/json' -d '{
  "name":"引风机A","plane_count":1,"point_count":1,"speeds":[1500],
  "phase_direction":"against_rotation","reference_deg":0}'

curl -s localhost:8080/api/v1/machines/<id>/jobs -d '{"operator":"张"}' \
  -H 'Content-Type: application/json'

curl -s localhost:8080/api/v1/jobs/<job>/runs -H 'Content-Type: application/json' -d '{
  "kind":"original","run_time":"2026-03-02T10:00:00Z",
  "readings":[{"speed":1500,"point":0,"amp_um":80,"phase_deg":30}]}'

curl -s localhost:8080/api/v1/jobs/<job>/runs -H 'Content-Type: application/json' -d '{
  "kind":"trial","run_time":"2026-03-02T10:01:00Z",
  "weights":[{"plane":0,"grams":20,"angle_deg":0}],
  "trial_plane":0,"trial_weight":{"grams":20,"angle_deg":0},
  "readings":[{"speed":1500,"point":0,"amp_um":89.44,"phase_deg":56.57}]}'

curl -s localhost:8080/api/v1/jobs/<job>
# result.speeds[0].influence = 2 μm/g ∠120°
# result.speeds[0].correction = 40 g ∠90°（去试重后单独加）
```

## 测试

```bash
go test ./...                 # 全部单元/流程/HTTP 测试（内存存储）
go test -race ./...
```

覆盖（对应需求清单）：

- 单面参考例子；方阵残余为零；任意配重往返自洽；
- 双面双测点合成算例（植造 2×2 H，两次试重精确还原、残余为零、验证通过）；
- 超定（1 面 3 测点）残余不劣于任一单测点平衡，且残差与 H 列正交；
  加权最小二乘信任/降权行为；
- 配重孔拆分（落在孔上/孔间、3–24 孔遍历角度合成恒等、口径换算下拆分）；
- 相位口径换算（参考偏移 + 沿/逆转向，内外往返）；
- 历史系数跨作业复用、失配失效后拒绝复用、重新试重恢复；
- 180° 录错后追加更正事件，结果与一次录对完全一致（含历史贡献）；
- 并发同刻提交两条均保留、按 seq 排序、更正消歧；
- 各类字段拒收（含小试重效应的动态判据）；多转速独立求解与历史分转速保存。

MySQL 集成测试（默认跳过）：设置 `BALANCE_TEST_MYSQL_DSN` 后运行
`TestMySQLIntegration`，验证真实 MySQL 8.0 上的事件持久化与重放一致性。
