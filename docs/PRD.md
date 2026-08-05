# Yêu cầu dự án: Universal Rate Limiting Service
### Centralized rate-limit & brute-force protection cho hệ thống multi-gateway (Kong / APISIX / Apigee)

**Phiên bản:** 0.1 (Draft)
**Ngày:** 05/08/2026
**Trạng thái:** Draft — chờ review

---

## 1. Bối cảnh & Vấn đề

Hệ thống hiện có nhiều API Gateway khác nhau (Kong, APISIX, Apigee...) đứng trước các service/API khác nhau. Mỗi gateway có engine rate-limit riêng (Kong: `rate-limiting`/`rate-limiting-advanced`; APISIX: `limit-req`/`limit-count`/`limit-conn`; Apigee: `SpikeArrest`/`Quota`), dẫn tới:

- **Policy không đồng nhất**: cùng một rule bảo mật (ví dụ "khóa IP sau 5 lần login sai/phút") phải cấu hình riêng lẻ ở từng gateway, dễ lệch nhau.
- **Không có góc nhìn tổng hợp**: không biết một IP/tài khoản đang bị giới hạn ở gateway nào, đã vi phạm bao nhiêu lần trên toàn hệ thống.
- **Khó mở rộng logic bảo mật**: muốn thêm lockout leo thang (progressive backoff) hay cảnh báo brute-force thì phải sửa N nơi, bằng N ngôn ngữ khác nhau (Lua cho Kong/APISIX, XML/JS cho Apigee).

**Mục đích dự án:** xây một service quyết định rate-limit tập trung ("rate-limit brain"). Các gateway chỉ gọi ra để hỏi "cho qua hay chặn", thay vì mỗi gateway tự tính toán độc lập.

---

## 2. Mục tiêu (Goals)

| # | Mục tiêu |
|---|----------|
| G1 | Một nơi duy nhất định nghĩa rule rate-limit, áp dụng nhất quán trên mọi gateway |
| G2 | Hỗ trợ limit theo tổ hợp key (IP, username/account, API key...) cùng lúc |
| G3 | Có cơ chế chuyên biệt cho brute-force: lockout tạm thời, leo thang thời gian khóa theo số lần vi phạm |
| G4 | Độ trễ thêm vào mỗi request ở mức chấp nhận được cho traffic thật (real-time, không phải batch) |
| G5 | Cắm được vào Kong, APISIX, Apigee mà không cần viết lại toàn bộ logic cho từng bên |
| G6 | Có audit log/metric để security team theo dõi các cuộc tấn công brute-force |

## 3. Ngoài phạm vi (Out of scope — v1)

- Thay thế hoàn toàn engine rate-limit gốc của từng gateway (v1 chạy song song, bổ sung cho case brute-force/security, không đụng tới quota kinh doanh sẵn có).
- Bot detection bằng ML/behavior analytics (như Apigee Advanced API Security) — để phase sau.
- WAF, chống DDoS tầng network (L3/L4).

## 4. Đối tượng liên quan

| Vai trò | Quan tâm chính |
|---|---|
| Platform/Infra team | Vận hành, độ ổn định, chi phí hạ tầng (Redis, service) |
| Security team | Định nghĩa rule, ngưỡng, xem audit log brute-force |
| Team backend (chủ API) | Độ trễ thêm vào, tính đúng đắn khi limit legit user |
| SRE/On-call | Fail-open/fail-closed khi service chết, alerting |

## 5. Yêu cầu chức năng (Functional Requirements)

| ID | Yêu cầu | Ưu tiên |
|---|---|---|
| FR1 | Expose API (gRPC + HTTP) dạng `CheckLimit(key, rule_id, cost) → {allow, remaining, retry_after}` | Must |
| FR2 | Hỗ trợ nhiều rule/nhiều khung thời gian trên cùng 1 key (vd 5 req/phút *và* 100 req/giờ) | Must |
| FR3 | Hỗ trợ composite key: IP-only, username-only, hoặc IP+username kết hợp | Must |
| FR4 | Hỗ trợ tối thiểu 2 thuật toán: sliding window (chính xác cao) và GCRA/token bucket (mượt burst) — chọn theo rule | Must |
| FR5 | Chế độ lockout leo thang cho brute-force: sau N lần vi phạm liên tiếp, thời gian khóa tăng dần (vd 1 phút → 5 phút → 30 phút) | Must |
| FR6 | Adapter riêng cho từng gateway: Kong (custom Lua plugin), APISIX (custom Lua plugin), Apigee (ServiceCallout + JavaScript policy) | Must |
| FR7 | Trả về header chuẩn hóa (`X-RateLimit-Limit/Remaining/Reset`, `Retry-After`) để mỗi adapter forward lại client | Should |
| FR8 | Admin API (CRUD rule) để cập nhật ngưỡng mà không cần redeploy gateway | Should |
| FR9 | Ghi log/metric mỗi lần block, gắn kèm gateway nguồn, key, rule vi phạm | Must |
| FR10 | Cấu hình fail-open/fail-closed theo từng rule khi service/Redis không phản hồi | Must |

## 6. Yêu cầu phi chức năng (Non-Functional Requirements)

| ID | Yêu cầu |
|---|---|
| NFR1 | Độ trễ thêm vào mỗi request ≤ 5ms ở p99 (khi service cùng vùng mạng với gateway) |
| NFR2 | Availability của rate-limit service ≥ 99.95% |
| NFR3 | Service stateless, scale ngang được; state dùng chung qua Redis Cluster |
| NFR4 | Thao tác đếm/kiểm tra phải atomic (Lua script trong Redis) để tránh race condition khi nhiều request đồng thời |
| NFR5 | Giao tiếp adapter ↔ service phải xác thực (mTLS hoặc API key riêng theo gateway) |
| NFR6 | Có metrics (Prometheus-compatible) + structured log cho observability |
| NFR7 | Hỗ trợ namespace theo môi trường/gateway để tránh đụng key giữa các hệ thống dùng chung Redis |

## 7. Kiến trúc đề xuất

```
┌─────────┐   ┌─────────┐   ┌─────────┐
│  Kong   │   │ APISIX  │   │ Apigee  │
│ adapter │   │ adapter │   │ adapter │   (thin, gateway-specific)
└────┬────┘   └────┬────┘   └────┬────┘
     │  gRPC/HTTP  │              │
     └─────────────┴──────┬───────┘
                           ▼
                ┌─────────────────────┐
                │ Rate-Limit Service  │  (stateless, scale ngang)
                │  - CheckLimit API   │
                │  - Rule engine      │
                │  - Lockout logic    │
                └──────────┬──────────┘
                           ▼
                   ┌───────────────┐
                   │ Redis Cluster │  (counter, lockout state)
                   └───────────────┘
```

- **Rate-Limit Service**: Go hoặc Node, chứa toàn bộ logic thuật toán + rule engine + lockout leo thang. Đây là phần dùng chung 100% giữa các gateway.
- **Adapter mỗi gateway**: chỉ làm 2 việc — (1) trích identifier phù hợp (IP, consumer/user sau khi gateway đó tự auth), (2) gọi `CheckLimit` và map response thành allow/block theo idiom riêng của gateway đó.
- **Redis Cluster**: lưu counter + trạng thái lockout, chia sẻ giữa mọi instance của service.

## 8. Ràng buộc & Giả định

- Giả định đã có (hoặc sẽ provision) Redis Cluster; nếu chưa, cần thêm task hạ tầng riêng.
- Giả định network latency giữa gateway và rate-limit service thấp (cùng region/VPC) — nếu gateway ở nhiều region xa nhau, cần tính lại NFR1.
- Apigee không cho chạy code biên dịch tùy ý → bắt buộc dùng ServiceCallout/JavaScript policy, không dùng chung binary/Wasm được như hướng Kong+APISIX.

## 9. Tiêu chí thành công (Acceptance Criteria)

- Cùng 1 rule định nghĩa 1 lần → hành vi block giống nhau khi test qua cả 3 gateway.
- Load test: giữ được throughput mục tiêu (cần chốt số cụ thể theo hệ thống thật) với độ trễ thêm vào trong ngưỡng NFR1.
- Test scenario brute-force: N lần login sai trong T giây → bị khóa đúng theo cấu hình leo thang, audit log ghi nhận đầy đủ.
- Khi giả lập sự cố rate-limit service/Redis → hệ thống xử lý đúng theo fail-open/fail-closed đã cấu hình, không crash gateway.

## 10. Rủi ro & giảm thiểu

| Rủi ro | Giảm thiểu |
|---|---|
| Rate-limit service trở thành single point of failure mới | Fail-open theo rule không critical; circuit breaker ở adapter; scale ngang + multi-AZ |
| Thêm latency ảnh hưởng UX | Đặt service cùng region, dùng connection pool/keep-alive, cân nhắc cache ngắn hạn kết quả "allow" ở adapter |
| Sai lệch khi trích identifier giữa các gateway | Viết test case riêng cho từng adapter, review kỹ phần map field trước khi rollout |

## 11. Lộ trình triển khai (đề xuất — cần chốt lại timeline thực tế)

1. **Phase 1**: Core service + Redis + rule engine cơ bản (sliding window), pilot trên Kong.
2. **Phase 2**: Thêm adapter APISIX, thêm thuật toán GCRA.
3. **Phase 3**: Thêm adapter Apigee (ServiceCallout/JS), chuẩn hóa header trả về.
4. **Phase 4**: Lockout leo thang đầy đủ + audit dashboard cho security team.

## 12. Phụ lục: Thuật ngữ

- **GCRA**: Generic Cell Rate Algorithm — thuật toán rate-limit mượt, tránh burst mà vẫn chính xác.
- **Sliding window**: đếm request trong khung thời gian trượt liên tục, chính xác hơn fixed window.
- **Fail-open/fail-closed**: khi hệ thống quyết định gặp lỗi, chọn cho qua (open) hay chặn (closed) mặc định.

---

## Phase 1 implementation notes (this repo)

This repository implements Phase 1 (core service + Redis + rule engine,
pilot adapters for all three gateways scaffolded together rather than
staged) plus a first cut of Phase 2-4 items:

| Requirement | Where |
|---|---|
| FR1 (gRPC + HTTP CheckLimit) | `proto/ratelimit/v1`, `internal/api/grpcserver`, `internal/api/httpserver` |
| FR2 (multi-window) | `internal/store/sliding_window.lua`, `internal/ruleengine` |
| FR3 (composite key) | `internal/ruleengine/key.go` |
| FR4 (sliding window + GCRA) | `internal/store/sliding_window.lua`, `internal/store/gcra.lua` |
| FR5 (progressive lockout) | `internal/store/lockout.lua`, `internal/ruleengine/engine.go` |
| FR6 (gateway adapters) | `adapters/kong`, `adapters/apisix`, `adapters/apigee` |
| FR7 (standard headers) | `internal/api/httpserver`, adapter code |
| FR8 (admin API) | `internal/api/adminserver`, `internal/ruleengine/registry.go` |
| FR9 (audit log/metrics) | `internal/audit`, `internal/metrics` |
| FR10 (fail-open/closed) | `internal/ruleengine/engine.go` (per-rule `fail_mode`) |
| NFR4 (atomic ops) | Redis Lua scripts in `internal/store/*.lua` |
| NFR5 (adapter auth) | `internal/api/authmw` |
| NFR7 (namespacing) | `config.RuleConfig.Namespace`, `internal/store` key hashing |

See the top-level `README.md` for how to run and test this locally.
