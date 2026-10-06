# 现场动平衡后端服务 — 设计说明

面向设备部的风机/泵/电机现场动平衡：影响系数法、单面或双面、1–4 个测点、多个工作转速；
按机器保存相位口径和影响系数，运行记录以事件流存储，所有结果可由事件重放得到。

技术栈：Go 1.23 + Echo，MySQL 8.0；无前端。

---

## 1. 目录结构（按职责分包）

| 包 | 职责 |
|---|---|
| `pkg/vec` | 振动矢量（幅值+相位）、相位口径（键相零点、顺/逆向）与内部统一坐标换算 |
| `pkg/solve` | 复数最小二乘（Householder QR 带列选主元）、影响系数拟合、校正配重求解、病态判定 |
| `pkg/split` | 固定等角度配重孔：把连续角度配重构造成相邻两孔，合成矢量严格不变 |
| `pkg/history` | 跨作业影响系数管理：逆方差融合、时效衰减、可信度判定 |
| `internal/domain` | 核心类型、字段校验规则、事件定义 |
| `internal/compute` | 事件重放与全部派生结果（内部坐标运行、逐转速系数、配重建议、孔拆分、信任判定） |
| `internal/store` | 存储接口、内存实现、MySQL 8 实现与迁移 |
| `internal/service` | 事务编排：校验 → 追加事件 → 重放 → 维护机器历史系数 |
| `internal/api` | Echo HTTP 接口与错误码映射 |
| `cmd/balancer` | 启动入口 |

---

## 2. 相位口径（`pkg/vec`）

**内部统一坐标**：数学约定，相位从键相标记起、沿**逆时针**增大，角度归一到 [0,360)。

每台机器保存一个 `Convention`：

- `zeroOffsetDeg`：现场"0°"（键相标记处）在内部坐标中的角度；
- `direction`：现场相位增大方向（+1 逆时针 / −1 顺时针）。

仿射换算（自身为对合变换）：

```
field    = dir · (internal − zeroOffset)
internal = zeroOffset + dir · field
```

配重角度、振动相位在入口（`projectRun`）换算为内部坐标，**所有计算只在内部坐标进行**；
出口（配重建议、孔角度、残余振动）再换回机器口径。配重孔孔号按现场口径表达：
0 号孔在现场 0°，角度沿现场正方向，与技工读数一致。

机器配置在建作业时快照进作业（`jobs.snapshot` JSON），此后修改机器口径不影响在途作业重放。

---

## 3. 线性模型与影响系数（`pkg/solve`）

### 3.1 模型

```
v = v0 + A·w
```

- `v0`：原始运行各测点（逐转速）振动，复数 µm；
- `w`：各校正面上的附加重量，复数 g（幅值与角度）；
- `A`：影响系数矩阵，行=测点×转速条件，列=校正面，复数 µm/g。

一次试重运行给出一个差分方程：`v_trial − v0 = A·w_trial`。
单面试一个重：`A = (v_trial − v0)/w_trial`。双面需要两个面各一次独立试重；
追加的修正运行可作为多余方程参与拟合。

### 3.2 超定方程组的求解准则：**加权最小二乘（WLS）**

测点数（条件数）多于校正面时方程超定。选择 **WLS（权重 2-范数残差最小）**，理由：

1. 现场最关心"整体振下来"，2-范数最小对应残余振动总能量最小；
2. WLS 解唯一、对读数噪声最优（噪声近似各向同性高斯时即最大似然），且有 QR 这样的
   稳定算法；
3. 权重接口保留：同一测点不同转速、或不同测点可以给行权重
   （例如关键轴承权重更大；当前默认全 1，机器上可配读数变化门槛）；
4. **不选整体最小二乘（TLS）**：试重块质量与角度是实物，误差远小于振动读数，
   TLS 对"两侧都有噪声"的假设在这里不成立且会引入额外偏差；
5. **不选切比雪夫（min-max）准则**作为默认：它只压低最差测点，容易牺牲其余测点，
   且对异常读数敏感。WLS 给出全局折中；需要"盯住最大残余"时，接口返回逐测点残余，
   现场可改为追加修正运行迭代。

**对"残余不大于任一测点单独平衡"的保证**：单独平衡第 p 个测点所得的一组配重是
WLS 可行域中的一个可行点；WLS 在全部可行配重中最小化残差 2-范数，因此其残差范数
必然不大于任何单点方案。测试 `TestOverdeterminedResidualBound` 直接验证这一点，
并验证最优性条件 `Aᴴr = 0`。

### 3.3 数值实现与自洽关系

- Householder 反射 QR（复数）+ Businger–Golob 列选主元；方阵走同一套代码，
  解精确到舍入误差。
- 参考例自洽：`v0=80∠30`，0° 加 20 g 后 `89.44∠56.57` ⇒ `A≈2∠120 µm/g`，
  校正 `w=−A⁻¹v0=40∠90 g`，代回预测残余为 0（测试 `TestReferenceSinglePlane`）。
- 往返自洽：任意 `w` → `v=Predict(A,v0,w)` → `RecoverWeights(A,v0,v)` 恒等恢复 `w`
  （方阵精确，超定为 WLS 意义下恢复）。
- 每次拟合同时给出：条件数（log10）、秩、逐测点残差方差 σ² 和每个系数的标准误
  `σ·sqrt((WᴴW)⁻¹_aa)`，供历史融合使用。

### 3.4 病态（试重效应太小）判据

两道关卡，任一不过即拒收并指出字段：

1. **单次试重效应门槛**（录入试重时）：每个转速下，至少有一个测点的振动变化
   `|v_trial−v0| ≥ max(floorUm, relFloor·|v0|)`，默认 `floorUm = 2 µm`、
   `relFloor = 5%`（机器可配 `readingChangeFloorUm`）。这道关卡保证试重效应高出
   读数噪声一个可辨识的量；报错信息给出转速与观测到的最大相对变化。
2. **矩阵条件数门槛**（两面试重齐了做拟合时）：QR 给出的条件数估计
   `log10(κ) > 3`（两个面须能区分到约 0.1%）或秩亏，一律判为病态
   （`ErrIllConditioned`），提示更换试重角度/大小。

### 3.5 多转速

每个转速独立拟合一个 `A_speed`，各自出单面/双面配重建议；另提供一个**多转速联合方案**：
把所有"测点×转速"条件堆叠成一个超定组求一组配重（一台转子只能装一组配重），
逐条件残余都返回。信任判定按转速分别进行，全部通过才算通过。

---

## 4. 配重孔拆分（`pkg/split`）

某面只有 `N` 个等角度孔（孔 0 在 0°，孔距 360/N）时，把目标配重 `m∠θ` 落到
夹着 θ 的相邻两孔。由正弦定理：

```
m1 = m·sin(Δ−β)/sin Δ
m2 = m·sin(β)/sin Δ
```

β 为 θ 相对第一孔的夹角（0..Δ）。两质量在孔内严格非负；θ 正落在孔上时整质量入该孔、
邻孔为 0。**合成矢量与目标矢量相等**（复数加法恒等，非近似），
由 `Recombine` 与测试 `TestSplitRecombine/TestSplitVectorExact` 验证。
孔数 < 3 在机器配置阶段就拒收。

---

## 5. 历史影响系数管理（`pkg/history`）

系数按"机器 × 转速"保存。

### 5.1 融合策略：逆方差融合 + 指数时效（选定方案）

每次含试重的作业在定稿/追加运行后产生一个新估计，逐元素带标准误 σ。
与历史系数融合：

```
w_old = 0.5^(ageDays / halfLife) / σ_old²
w_new = 1 / σ_eff,new²           σ_eff = max(σ_fit, σ_abs + σ_rel·|A|)
A_fused = (w_old·A_old + w_new·A_new)/(w_old + w_new)
σ_fused = 1/sqrt(w_old + w_new)，且不低于策略下限
```

- 默认半衰期 365 天：近期、高精度估计主导；多年前的经验自动淡出而非硬删。
- 不确定度设下限（绝对 0.05 µm/g + 相对 5%），防止一次"过于完美"的拟合把后续信息锁死。
- 三种候选里选择它而非"只用最新一次"（浪费历史、抗噪差）或"简单滑动平均"
  （忽略每次估计质量差异）：逆方差融合是高斯假设下的最优无偏融合，且天然处理
  观测次数不等的作业。

### 5.2 何时判定历史系数失效（必须重新试重）

使用历史系数跳过试重的作业，其**验证运行**要过两关（逐测点、逐转速）：

1. 模型误差：`|v_meas − v_pred| / max(|v0|,ε) ≤ 35%`
   （预测振动与实测一致——口径没反、机器没大变）；
2. 降幅：当初始振动显著（默认 ≥10 µm）时，`1 − |v_meas|/|v0| ≥ 50%`
   （平衡确实见效）。

任一不过：当前系数标记 **suspect**（不确定度放大 10 倍，不再提供复用），
`useHistory=true` 的新作业直接被拒（409），必须重新试重；此后一次通过验证的
全新试重估计**替换**而非融合 suspect 条目，恢复为 active。
小振幅测点只查模型误差，避免拿接近平衡的转子做无意义的降幅比较。

### 5.3 更正后历史系数如何更新

机器当前系数不是增量补丁，而是在每次写入/更正事件后，**按时间顺序回放该机器全部作业**
重建：作业按最后事件时间排序，逐个重放，信任失败的历史作业在时间轴上把当时系数置
suspect，其后合格作业开启新条目。因此更正某台机器某次旧作业的读数后，
机器历史系数与"当时就录对"完全一致（见 `TestServiceCorrectionEquivalence`，
逐元素比较两台独立构建的机器系数）。

---

## 6. 事件存储与重放（`internal/domain` + `internal/compute`）

一个作业一个事件流（`job_events`，按作业独立连续编号 `seq`）：

- `job_created`：作业与机器布局/口径快照；
- `run_added`：一次运行（种类、运行时间、当时全部附加重量、各测点各转速读数）；
- `run_corrected`：对某次运行的更正（只覆盖提交的字段）；**原始事件不删除、不修改**；
- `job_finalized`：作业关闭。

运行种类：`original` / `trial`（每面各一次）/ `verify`（验证，不参与拟合）/
`correction`（追加修正，参与拟合）。

**重放规则**：

1. 按事件顺序应用增改，得到每次运行的最终内容；
2. 运行按**现场运行时间 `runAt` 排序**（seq 仅作并列决胜）——两名技术员并发提交时
   两条都保留，顺序以运行时间为准（`TestConcurrentSubmissionsOrderedByRunTime`、
   `TestHTTPConcurrentRuns`）；
3. 投影到内部坐标后，重新拟合各转速影响系数、重算配重建议、孔拆分、多转速方案、
   追加修正方案和信任判定。

因为投影是纯函数，"先录错再用 `run_corrected` 更正"与"一开始就录对"
产生**逐位相同**的结果（系数、配重、机器历史均验证一致）。

并发控制：写事务先 `SELECT … FOR UPDATE` 锁住作业行，再取 `job_event_seq`
（`UPDATE … LAST_INSERT_ID(last_seq+1)`，行锁），因此并发写被序列化、seq 在作业内
连续无跳号；客户端可带 `clientId` 做幂等重试（重复提交返回同一 run，不新增事件）。

---

## 7. 校验与拒收（字段级，HTTP 422）

| 规则 | 字段 |
|---|---|
| 幅值为负 | `weights[i].polar.amp` / `readings[i].polar.amp` |
| 相位不在 [0,360) | `*.polar.phase` |
| 校正面数超出 1–2 | `planes` |
| 测点数超出 1–4 | `points` |
| 配重孔数 < 3（0 表示任意角度，允许） | `planes[i].holeCount` |
| 试重为零 | `weights` |
| 试重与原始运行读数差太小 | `readings`（附转速与判据数值） |
| 未知面/点/转速、重复读数、读数缺测点×转速组合 | 对应下标字段 |
| 原始运行带重量、重复原始运行、先于原始运行提交其他运行 | `kind` / `weights`（409） |

错误响应：`{"error": ..., "fields":[{"field":..., "reason":...}]}`。

---

## 8. HTTP 接口

```
GET  /healthz
POST /api/v1/machines                       建机器（口径、面、点、转速、孔数）
GET  /api/v1/machines
GET  /api/v1/machines/:id
GET  /api/v1/machines/:id/coefficients      逐转速当前系数与状态(active/suspect)

POST /api/v1/jobs                           {machineId, useHistory}
GET  /api/v1/jobs/:id                       作业 + 全部派生状态
POST /api/v1/jobs/:id/runs                  提交运行（可带 clientId 幂等）
POST /api/v1/jobs/:id/runs/:runId/corrections  更正某次运行（新事件）
POST /api/v1/jobs/:id/finalize
```

作业状态响应包含：时间序运行列表、逐转速影响系数（外部极坐标+不确定度+条件数）、
逐转速配重建议（外部角度、预测残余、相邻孔拆分及合成量）、多转速联合方案、
追加修正方案（在当前已装重量上再加多少）、信任判定明细、定稿信息。

---

## 9. 存储

MySQL 8（`migrations/0001_init.sql`）：

- `machines` / `jobs`（快照 JSON）；
- `job_event_seq`（每作业连续 seq，事务内 `LAST_INSERT_ID` 取号）；
- `job_events`（事件 JSON，`(job_id, client_id)` 唯一用于幂等）；
- `job_contributions`（各作业逐转速估计，留档可审计）；
- `coeff_current`（重建出的当前融合系数，供后续作业复用）。

另提供等价的内存后端（`BALANCER_STORE=memory`，零依赖跑测试/演示）。
启动时自动建库并按文件名顺序执行迁移。

---

## 10. 测试覆盖（`go test ./...`）

| 需求点 | 测试 |
|---|---|
| 单面参考例子（2∠120、40∠90） | `solve.TestReferenceSinglePlane`、`compute.TestReplayReferenceExample`、`service.TestServiceReferenceExample`、`api.TestHTTPReferenceExample` |
| 方阵残余为零 | `solve.TestSquareZeroResidual`、`compute.TestTwoPlaneTwoPoint` |
| 往返自洽 | `solve.TestRoundTrip` |
| 双面双测点合成算例 | `solve.TestTwoPlaneTwoPointFit`、`compute.TestTwoPlaneTwoPoint`、`service.TestServiceTwoPlaneTwoPoint` |
| 超定残余不劣于单点平衡 | `solve.TestOverdeterminedResidualBound`、`compute.TestOverdeterminedViaReplay`、`service.TestServiceOverdeterminedBound` |
| 配重孔拆分（合成不变、非负、落孔） | `split` 全部、`compute.TestHoleSplitInPlan`、`service.TestServiceHoleSplit` |
| 相位口径换算 | `vec.TestConvention*`、`compute.TestReplayConventionConversion` |
| 历史系数复用与失效判定 | `history.TestTrustPassAndFail`、`compute.TestHistoryReuseAndInvalidation`、`service.TestServiceHistoryReuseAndFailure` |
| 读数更正与录对完全一致 | `compute.TestCorrectionEventEquivalence`、`service.TestServiceCorrectionEquivalence`、`api.TestHTTPCorrection` |
| 并发提交（都保留、按运行时间排序、幂等） | `compute.TestConcurrentSubmissionsOrderedByRunTime`、`service.TestServiceConcurrentSubmissions`、`api.TestHTTPConcurrentRuns` |
| 拒收字段矩阵 | `service.TestServiceRejections`、`api.TestHTTPRejections` |
| 多转速逐转速+联合系数 | `service.TestServiceMultiSpeed` |
| 融合（逆方差、老化、suspect 替换） | `history.TestFuse*`、`TestSuspectReplaced` |

## 11. 本地运行

```bash
docker compose up --build            # MySQL 8 + 服务，:8080
# 或无数据库演示：
BALANCER_STORE=memory go run ./cmd/balancer
```
