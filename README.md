# mosdns

功能概述、配置方式、教程等，详见 [mosdns v5 Wiki](https://irine-sistiana.gitbook.io/mosdns-wiki/mosdns-v5)。

## 本仓库的自定义插件

本源码包含两个实验性自定义插件，针对大规模域名规则的本地分流场景设计。它们不是官方 mosdns 插件，升级官方源码时需要重新审查兼容性。

### `dat_domain_set`

- 位置：`pkg/matcher/dat/`、`plugin/data_provider/dat_domain_set/`。
- 通过 `files:` 读取多个规则文件，并构建一棵倒序域名 DAT（Double-Array Trie）。
- 支持普通域名、`domain:` 和 `full:`；当前实现不会执行 `keyword:`、`regexp:`，使用这两类规则时必须先转换或继续使用官方 `domain_set`。
- 可通过 `cache_file` 保存二进制结构；缓存应只作为加速，损坏或不匹配时应重新构建。
- 缓存与规则内容、表达式和文件路径相关；增删文件或修改 `exps` 后应删除对应 `.bin` 及 `.meta` 后重启。
- 查询阶段为只读数组访问；构建阶段仍可能消耗较多 CPU 和内存。

示例：

```yaml
- tag: all_local_domains
  type: dat_domain_set
  args:
    cache_file: "/etc/mosdns/dat_all_local.bin"
    files:
      - "/etc/mosdns/rule/geosite_cn.txt"
      - "/etc/mosdns/rule/tld_cn.txt"
```

### `match_cache`

- 位置：`plugin/executable/match_cache/match_cache.go`。
- 缓存域名分流路径，不缓存 DNS 应答；命中后仍会执行目标 sequence，因此可继续转发到 AdGuard Home。
- 使用 16 个分片和 `RWMutex`，不是无锁缓存；容量按配置值分配，驱逐策略和 chain 跳转语义应结合当前 mosdns 配置审查。
- 缓存通常只适合不依赖客户端、ECS、qtype 等上下文的 qname 分流。规则变更后应重启 mosdns 或清空缓存。
- `match_cache_record` 用于记录规则命中的目标路径；目标插件必须与当前实现支持的 sequence/执行链语义一致。

## 编译和运行要求


- DAT 构建需要额外内存；运行时内存取决于规则规模、DAT 数组和 `match_cache` 容量。
- 二进制缓存不是跨版本稳定格式；修改 DAT 文件格式或插件代码后应删除旧缓存并重新生成。

官方项目与发布信息：
- [官方 Wiki](https://irine-sistiana.gitbook.io/mosdns-wiki/mosdns-v5)
- [官方源码](https://github.com/IrineSistiana/mosdns)
- [官方发布页](https://github.com/IrineSistiana/mosdns/releases)

本仓库不包含测试或 benchmark 文件；性能数字应以实际目标机器上的独立基准测试为准。 

### DAT 规则去重

`dat_domain_set` 在同一个插件实例的 `files:` 和 `exps:` 输入范围内，会对规范化后的规则去重后再写入 DAT：

- 去除首尾空白、统一小写并去除域名末尾的 `.` 后参与比较；
- `domain:` 和普通域名按规则类型分别识别；
- 相同规则跨多个 `files:` 文件只插入一次；
- `full:` 与 `domain:` 即使域名文本相同，也视为不同规则类型；
- 去重只发生在内存建树阶段，不修改磁盘上的原始规则文件；
- 已存在的二进制缓存不会自动因新增去重逻辑而重建；若要确保重新构建，请删除对应的 `.bin` 和 `.meta` 后重启 mosdns。

去重集合属于单个 `Builder`，不同 `dat_domain_set` 实例之间不会共享规则或去重状态。
