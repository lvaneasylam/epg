## goepg v1.0.7

### 更新

- 限制首页使用缓存：页面请求加时间戳参数，始终获取服务端最新数据
- 提高响应速度 50%

### 升级方式

```bash
tar -xzf goepg_v1.0.7_linux_amd64.tar.gz
# config.yaml 需包含（v1.0.6 起的热缓存前提，已配置则跳过）：
#   icon:
#     dir: "data/icons"
#     base_url: "https://epg.c-hiang.cn"
./goepg -c config.yaml
```

### 下载说明

| 文件 | 适用 |
|---|---|
| `goepg_v1.0.7_linux_amd64.tar.gz` | x86_64 服务器（常用） |
| `goepg_v1.0.7_linux_arm64.tar.gz` | ARM 服务器 / NAS |

压缩包内含 `goepg` 二进制与 `config.example.yaml`；完整性校验见 `SHA256SUMS`。
