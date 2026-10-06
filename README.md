# balancer — 现场动平衡后端服务

风机 / 泵 / 电机现场动平衡（影响系数法）后端：记录每台机器的相位口径与影响系数、
单面/双面、1–4 测点、多转速；运行记录以事件流存储，影响系数、配重建议、
固定配重孔拆分全部由事件重放推导，读数更正可做到与"一开始就录对"完全一致。

详细设计与算法选型理由见 [DESIGN.md](DESIGN.md)。

## 技术栈

Go 1.23 · Echo · MySQL 8.0 · Docker Compose（无前端）

## 一键启动

```bash
docker compose up --build
# 服务: http://localhost:8080
```

无数据库的本地演示（内存存储，重启清空）：

```bash
go run ./cmd/balancer            # 默认 :8080, BALANCER_STORE=memory
```

环境变量：`HTTP_ADDR`、`BALANCER_STORE`（mysql|memory）、`MYSQL_DSN`、`MIGRATIONS_DIR`。

## 快速试一遍核对用单面例子

```bash
# 1. 建机器：1 个校正面（12 个等角度配重孔）、1 个测点、1500 rpm
curl -s -X POST localhost:8080/api/v1/machines -H 'Content-Type: application/json' -d '{
  "name":"引风机A",
  "planes":[{"id":"P1","name":"叶轮","holeCount":12}],
  "points":[{"id":"V1","name":"驱动端水平"}],
  "speedsRpm":[1500],
  "convention":{"zeroOffsetDeg":0,"direction":1}
}'

# 2. 建作业（返回 job.id）
curl -s -X POST localhost:8080/api/v1/jobs -H 'Content-Type: application/json' \
  -d '{"machineId":"<mac_id>"}'

# 3. 原始运行：80 μm∠30°
curl -s -X POST localhost:8080/api/v1/jobs/<job_id>/runs -H 'Content-Type: application/json' -d '{
  "kind":"original","runAt":"2026-10-06T10:00:00Z","weights":[],
  "readings":[{"pointId":"V1","speedRpm":1500,"polar":{"amp":80,"phase":30}}]}'

# 4. 试重运行：0° 加 20 g 后 89.44 μm∠56.57°
curl -s -X POST localhost:8080/api/v1/jobs/<job_id>/runs -H 'Content-Type: application/json' -d '{
  "kind":"trial","runAt":"2026-10-06T10:05:00Z",
  "weights":[{"planeId":"P1","polar":{"amp":20,"phase":0}}],
  "readings":[{"pointId":"V1","speedRpm":1500,"polar":{"amp":89.44,"phase":56.57}}]}'
```

返回的 `state` 中：

- `fits[0].influence[0][0].polar` ≈ **2 μm/g∠120°**（影响系数）；
- `plans[0].weights[0]` ≈ **40 g∠90°**（去试重后单独加的校正配重），
  并给出 12 孔拆分（相邻 60°/90° 两孔，合成矢量与 40∠90 相同）；
- `plans[0].predictedResidual[0].ampUm` ≈ 0。

## 主要接口

| 方法 路径 | 说明 |
|---|---|
| `POST /api/v1/machines` | 建机器（口径、面/点/转速、孔数） |
| `GET  /api/v1/machines/:id/coefficients` | 逐转速当前影响系数（active/suspect） |
| `POST /api/v1/jobs` | 开作业（`useHistory:true` 复用历史系数、免试重） |
| `GET  /api/v1/jobs/:id` | 作业 + 全部派生结果（系数、配重、孔拆分、信任判定） |
| `POST /api/v1/jobs/:id/runs` | 提交运行（`kind`: original/trial/verify/correction；可带 `clientId` 幂等） |
| `POST /api/v1/jobs/:id/runs/:runId/corrections` | 更正某次运行（作为新事件，结果与录对一致） |
| `POST /api/v1/jobs/:id/finalize` | 定稿 |

相位口径 `convention`：`zeroOffsetDeg` = 现场 0°（键相处）的内部角度；
`direction` = 现场相位增大方向（1 逆时针、−1 顺时针）。
配重角度与读数相位都按该机器口径提交，服务内部统一换算。

## 校验拒收（HTTP 422，错误体带字段名）

幅值为负、相位不在 [0,360)、校正面数不在 1–2、测点数不在 1–4、
配重孔数 < 3、试重为零、试重与原始读数差别太小（判据写在错误信息里）。

## 测试

```bash
go test ./...

# MySQL 集成测试（默认跳过；用 compose 起好库后执行）
BALANCER_TEST_MYSQL_DSN='root:rootpw@tcp(127.0.0.1:3306)/?parseTime=true' \
MIGRATIONS_DIR=migrations go test ./internal/store -run MySQL -v
```

## 包结构

```
pkg/vec       矢量与相位口径换算
pkg/solve     复数加权最小二乘(QR)、影响系数拟合、校正求解、病态判定
pkg/split     固定配重孔拆分（合成矢量不变）
pkg/history   影响系数逆方差融合、时效衰减、可信度判定
internal/domain   类型、校验、事件
internal/compute  事件重放与派生计算
internal/store    内存 / MySQL 8 存储与迁移
internal/service  事务化业务编排
internal/api      Echo HTTP 接口
cmd/balancer      启动入口
```
