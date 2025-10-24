# Kratos Nacos Integration

一个为 Kratos 框架提供 Nacos 集成的 Go 模块，支持服务注册发现和配置中心功能。

## 特性

-  **服务注册与发现**：完整实现 Kratos registry.Registrar 和 registry.Discovery 接口
-  **配置中心**：实现 Kratos config.Source 接口，支持配置热更新
-  **认证支持**：支持用户名密码认证
-  **日志配置**：集成 nacos-sdk-go 日志配置
-  **多端点支持**：支持 HTTP、gRPC 等多种协议
-  **Go Workspace 兼容**：支持 Go 1.24+ 和 Go Workspace

## 项目结构

```
kratos-nacos/
├── nacos/
│   ├── client.go      # Nacos 客户端封装
│   ├── config.go      # 配置中心实现
│   ├── registry.go    # 服务注册发现实现
│   └── options.go     # 配置选项
├── go.mod
└── README.md
```

## 安装

```bash
go get github.com/snac21/kratos-nacos
```

## 快速开始

### 1. 基本使用

```go
package main

import (
    "github.com/go-kratos/kratos/v2"
    "github.com/go-kratos/kratos/v2/config"
    "github.com/snac21/kratos-nacos/nacos"
)

func main() {
    // 1. 创建 Nacos 客户端
    nacosClient, err := nacos.NewClient(
        nacos.WithHost("127.0.0.1:8848"),                    // Nacos 地址
        nacos.WithNamespaceId("public"),                     // 命名空间
        nacos.WithConfigDataID("your-app.yaml"),           // 配置文件 DataID
        nacos.WithConfigGroup("DEFAULT_GROUP"),              // 配置分组
        nacos.WithRegistryGroup("DEFAULT_GROUP"),            // 服务注册分组
        nacos.WithLogLevel("info"),                          // 日志级别
    )
    if err != nil {
        panic(err)
    }

    // 2. 使用 Nacos 作为配置源
    configSource := nacos.NewConfigSource(nacosClient)
    c := config.New(
        config.WithSource(configSource),
    )
    defer c.Close()

    if err := c.Load(); err != nil {
        panic(err)
    }

    // 3. 创建 Kratos 应用，使用 Nacos 作为注册中心
    app := kratos.New(
        kratos.Name("my-service"),
        kratos.Registrar(nacosClient), // 使用 Nacos 客户端作为注册器
        // ... 其他选项
    )

    if err := app.Run(); err != nil {
        panic(err)
    }
}
```

### 2. 带认证的使用

```go
// 创建带认证的 Nacos 客户端
nacosClient, err := nacos.NewClient(
    nacos.WithHost("127.0.0.1:8848"),
    nacos.WithNamespaceId("public"),
    nacos.WithConfigDataID("your-app.yaml"),
    nacos.WithConfigGroup("DEFAULT_GROUP"),
    nacos.WithRegistryGroup("DEFAULT_GROUP"),
    nacos.WithUsername("your_username"),                     // 用户名
    nacos.WithPassword("your_password"),                     // 密码
    nacos.WithLogLevel("error"),                             // 日志级别
    nacos.WithLogDir("./logs"),                              // 日志目录
    nacos.WithCacheDir("./cache"),                           // 缓存目录
)
```

### 3. 完整的 Kratos 应用示例

```go
package main

import (
    "flag"
    "os"

    "vehicleDataService/internal/conf"
    pkglog "vehicleDataService/internal/pkg/log"

    "github.com/go-kratos/kratos/v2"
    "github.com/go-kratos/kratos/v2/config"
    "github.com/go-kratos/kratos/v2/log"
    "github.com/go-kratos/kratos/v2/middleware/tracing"
    "github.com/go-kratos/kratos/v2/registry"
    "github.com/go-kratos/kratos/v2/transport/grpc"
    "github.com/go-kratos/kratos/v2/transport/http"
    "github.com/snac21/kratos-nacos/nacos"
)

var (
    Name    = "ServiceName"
    Version = "1.0.0"
    id, _   = os.Hostname()
)

func main() {
    flag.Parse()

    // 1. 初始化 Nacos 客户端
    nacosClient, err := nacos.NewClient(
        nacos.WithHost("127.0.0.1:8848"),
        nacos.WithNamespaceId("public"),
        nacos.WithConfigDataID("your-app.yaml"),
        nacos.WithConfigGroup("DEFAULT_GROUP"),
        nacos.WithRegistryGroup("DEFAULT_GROUP"),
        nacos.WithUsername("username"),
        nacos.WithPassword("password"),
        nacos.WithLogLevel("error"),
        nacos.WithLogDir("./logDir"),
        nacos.WithCacheDir("./cacheDir"),
    )
    if err != nil {
        panic(err)
    }

    // 2. 创建 Kratos Config，使用 Nacos 作为配置源
    configSource := nacos.NewConfigSource(nacosClient)
    c := config.New(
        config.WithSource(configSource),
    )
    defer c.Close()

    if err := c.Load(); err != nil {
        panic(err)
    }

    var bc conf.Bootstrap
    if err := c.Scan(&bc); err != nil {
        panic(err)
    }

    // 3. 初始化日志器
    var baseLogger log.Logger
    if bc.Log != nil {
        baseLogger = pkglog.NewLogger(bc.Log)
    } else {
        baseLogger = log.NewStdLogger(os.Stdout)
    }

    logger := log.With(baseLogger,
        "caller", log.DefaultCaller,
        "service.id", id,
        "service.name", Name,
        "service.version", Version,
        "trace.id", tracing.TraceID(),
        "span.id", tracing.SpanID(),
    )

    // 4. 创建应用（通过 Wire 注入依赖）
    app, cleanup, err := wireApp(bc.Server, bc.Data, logger, nacosClient)
    if err != nil {
        panic(err)
    }
    defer cleanup()

    // 5. 启动应用
    if err := app.Run(); err != nil {
        panic(err)
    }
}

func newApp(logger log.Logger, hs *http.Server, gs *grpc.Server, registrar registry.Registrar) *kratos.App {
    return kratos.New(
        kratos.ID(id),
        kratos.Name(Name),
        kratos.Version(Version),
        kratos.Metadata(map[string]string{}),
        kratos.Logger(logger),
        kratos.Server(hs, gs),
        kratos.Registrar(registrar), // 使用 Nacos 作为注册器
    )
}
```

## 配置选项

### 可用的配置选项

| 选项函数 | 参数类型 | 描述 | 默认值 |
|---------|---------|------|--------|
| `WithHost` | `...string` | Nacos 服务器地址（支持多个） | 必填 |
| `WithNamespaceId` | `string` | 命名空间 ID | `"public"` |
| `WithConfigDataID` | `string` | 配置文件 DataID | `""` |
| `WithConfigGroup` | `string` | 配置分组 | `"DEFAULT_GROUP"` |
| `WithRegistryGroup` | `string` | 服务注册分组 | `"DEFAULT_GROUP"` |
| `WithRegistryClusters` | `...string` | 服务注册集群 | `[]` |
| `WithWeight` | `float64` | 服务实例权重 | `10.0` |
| `WithUsername` | `string` | 认证用户名 | `""` |
| `WithPassword` | `string` | 认证密码 | `""` |
| `WithLogLevel` | `string` | 日志级别 | `"info"` |
| `WithLogDir` | `string` | 日志目录 | `"tmp/nacos/log"` |
| `WithCacheDir` | `string` | 缓存目录 | `"tmp/nacos/cache"` |

### 使用示例

```go
// 多服务器配置
nacosClient, err := nacos.NewClient(
    nacos.WithHost("192.168.1.10:8848", "192.168.1.11:8848"),
    nacos.WithNamespaceId("production"),
    nacos.WithRegistryClusters("cluster1", "cluster2"),
    nacos.WithWeight(100.0),
)

// 自定义日志和缓存目录
nacosClient, err := nacos.NewClient(
    nacos.WithHost("127.0.0.1:8848"),
    nacos.WithLogLevel("debug"),
    nacos.WithLogDir("/var/log/nacos"),
    nacos.WithCacheDir("/var/cache/nacos"),
)
```

## 核心功能

### 1. 配置中心

实现了 Kratos `config.Source` 接口，支持：

- 从 Nacos 加载配置文件
- 配置热更新（通过 `Watch()` 方法）
- 支持 YAML 格式配置

```go
// 创建配置源
configSource := nacos.NewConfigSource(nacosClient)

// 使用配置源
c := config.New(config.WithSource(configSource))
```

### 2. 服务注册与发现

实现了 Kratos `registry.Registrar` 和 `registry.Discovery` 接口，支持：

- 服务注册和注销
- 服务发现
- 健康检查
- 多端点支持（HTTP、gRPC）

```go
// 作为注册器使用
app := kratos.New(
    kratos.Name("my-service"),
    kratos.Registrar(nacosClient),
)

// 作为发现器使用
instances, err := nacosClient.GetService(ctx, "target-service")
```

### 3. 日志集成

自动配置 nacos-sdk-go 的日志输出：

```go
// 日志配置会自动应用到 nacos-sdk-go
nacosClient, err := nacos.NewClient(
    nacos.WithLogLevel("error"),    // 设置日志级别
    nacos.WithLogDir("./logs"),     // 设置日志目录
)
```
