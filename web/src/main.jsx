import React, { useState as S, useEffect } from "react";
import { createRoot } from "react-dom/client";
import {
  Button,
  Input,
  Modal,
  Switch,
  Card,
  Space,
  Alert,
  Typography,
  Tag,
} from "antd";
import "antd/dist/reset.css";
import "./style.css";
async function api(path, method = "GET", body) {
  const r = await fetch("/api" + path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = r.status === 204 ? {} : await r.json();
  if (!r.ok) throw Error(data.error || "请求失败");
  return data;
}
function App() {
  const [view, V] = S(location.pathname === "/tokens" ? "tokens" : "instances"),
    [instances, I] = S([]),
    [tokens, T] = S([]),
    [def, D] = S(""),
    [error, E] = S(""),
    [form, F] = S(null),
    [secret, Q] = S(""),
    [del, X] = S(null),
    [status, ST] = S({}),
    [browse, B] = S(null),
    [objects, O] = S([]),
    [cursor, C] = S(""),
    [columns, CL] = S(null);
  async function run(fn) {
    try {
      E("");
      await fn();
    } catch (e) {
      E(e.message);
    }
  }
  async function load() {
    const [a, b, c] = await Promise.all([
      api("/instances"),
      api("/tokens"),
      api("/settings"),
    ]);
    I(a.instances || []);
    T(b.tokens || []);
    D(c.default_instance || "");
  }
  useEffect(() => {
    run(load);
  }, []);
  function nav(v) {
    V(v);
    B(null);
    CL(null);
    Q("");
    history.pushState({}, "", "/" + v);
  }
  async function listing(id, cur = "", group = "") {
    const p = new URLSearchParams({ page_size: "1000" });
    if (cur) p.set("cursor", cur);
    if (group) p.set("group_id", group);
    const d = await api(
      "/instances/" + encodeURIComponent(id) + "/objects?" + p,
    );
    B({ id, group });
    O(d.objects || []);
    C(d.next_cursor || "");
    CL(null);
  }
  return (
    <main>
      <header>
        <div>
          <Typography.Title level={2}>SQLX</Typography.Title>
          <p>数据库连接与结构管理</p>
        </div>
        <Space>
          <Button data-testid="nav-instances" onClick={() => nav("instances")}>
            数据库实例
          </Button>
          <Button data-testid="nav-tokens" onClick={() => nav("tokens")}>
            访问令牌
          </Button>
        </Space>
      </header>
      {error && (
        <Alert
          type="error"
          showIcon
          message={error}
          closable
          onClose={() => E("")}
        />
      )}
      {view === "instances" ? (
        <>
          <h2>数据库实例</h2>
          <Button
            type="primary"
            data-testid="instance-add"
            onClick={() =>
              F({ kind: "instance", id: "", name: "", dsn: "", memo: "" })
            }
          >
            添加实例
          </Button>
          <section>
            {instances.map((i) => (
              <Card
                key={i.id}
                data-testid={"instance-row-" + i.id}
                title={i.name || i.id}
              >
                <p>
                  {i.id} · {i.memo}
                </p>
                <Space wrap>
                  <Tag
                    data-testid="instance-status"
                    data-status={status[i.id]?.ok ? "success" : "unknown"}
                  >
                    {status[i.id]
                      ? status[i.id].ok
                        ? "连接成功"
                        : status[i.id].error
                      : i.status || "未测试"}
                  </Tag>
                  <Tag
                    data-testid="instance-default-state"
                    data-default={String(def === i.id)}
                  >
                    {def === i.id ? "默认实例" : "非默认"}
                  </Tag>
                  <Button
                    data-testid="instance-test"
                    onClick={() =>
                      run(async () => {
                        const r = await api(
                          "/instances/" + i.id + "/test",
                          "POST",
                        );
                        ST((s) => ({ ...s, [i.id]: r }));
                      })
                    }
                  >
                    测试连接
                  </Button>
                  <Button
                    data-testid="instance-default"
                    onClick={() =>
                      run(async () => {
                        await api("/settings/default", "PUT", {
                          instance: i.id,
                        });
                        await load();
                      })
                    }
                  >
                    设为默认
                  </Button>
                  <Button
                    data-testid="instance-browse"
                    onClick={() => run(() => listing(i.id))}
                  >
                    浏览结构
                  </Button>
                  <Button
                    disabled={i.source && i.source !== "managed"}
                    onClick={() =>
                      F({ ...i, kind: "instance", edit: true, dsn: "" })
                    }
                  >
                    编辑
                  </Button>
                  <Button
                    danger
                    data-testid="instance-delete"
                    disabled={i.source && i.source !== "managed"}
                    onClick={() => X({ path: "/instances/" + i.id })}
                  >
                    删除
                  </Button>
                </Space>
              </Card>
            ))}
          </section>
          {browse && (
            <Card title="表与视图">
              {objects.map((o, n) => (
                <Button
                  key={n}
                  data-testid={"table-row-" + o.name}
                  onClick={() =>
                    run(async () => {
                      if (o.kind === "table_group")
                        return listing(browse.id, "", o.group_id);
                      CL(
                        await api(
                          "/instances/" +
                            browse.id +
                            "/objects/describe?" +
                            new URLSearchParams({
                              object_name: o.name,
                              object_type: o.type || "table",
                            }),
                        ),
                      );
                    })
                  }
                >
                  {o.kind === "table_group"
                    ? o.prefix + "（疑似分表 " + o.member_count + "）"
                    : o.name}
                </Button>
              ))}
              {cursor && (
                <Button
                  onClick={() =>
                    run(() => listing(browse.id, cursor, browse.group))
                  }
                >
                  下一页
                </Button>
              )}
              {columns && (
                <pre data-testid="table-columns">
                  {JSON.stringify(columns, null, 2)}
                </pre>
              )}
            </Card>
          )}
        </>
      ) : (
        <>
          <h2>访问令牌</h2>
          <p>数据库账号权限是最终读写边界。</p>
          <Button
            data-testid="token-add"
            type="primary"
            onClick={() => {
              Q("");
              F({ kind: "token", name: "", allow_execute: false });
            }}
          >
            创建令牌
          </Button>
          {secret && (
            <Alert
              message="请立即保存，令牌仅展示一次"
              description={<code data-testid="token-secret">{secret}</code>}
            />
          )}
          <section>
            {tokens.map((t) => (
              <Card
                key={t.id}
                data-testid={"token-row-" + t.name}
                title={t.name}
              >
                <Tag>{t.allow_execute ? "允许修改数据" : "只读"}</Tag>
                <Button
                  danger
                  data-testid="token-revoke"
                  onClick={() => X({ path: "/tokens/" + t.id })}
                >
                  撤销
                </Button>
              </Card>
            ))}
          </section>
        </>
      )}
      <Modal
        open={!!form}
        title={
          form?.kind === "token"
            ? "创建访问令牌"
            : form?.edit
              ? "编辑实例"
              : "添加实例"
        }
        onCancel={() => F(null)}
        footer={null}
        destroyOnHidden
      >
        {form && (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              run(async () => {
                if (form.kind === "token") {
                  const r = await api("/tokens", "POST", {
                    name: form.name,
                    allow_execute: form.allow_execute,
                  });
                  Q(r.token);
                } else {
                  const { kind, edit, dsn, ...rest } = form;
                  const body = { name: rest.name, memo: rest.memo };
                  if (!edit) body.id = rest.id;
                  if (dsn) body.dsn = dsn;
                  await api(
                    "/instances" + (edit ? "/" + rest.id : ""),
                    edit ? "PATCH" : "POST",
                    body,
                  );
                }
                F(null);
                await load();
              });
            }}
          >
            {form.kind === "instance" && (
              <label>
                ID
                <Input
                  required
                  disabled={form.edit}
                  data-testid="instance-id"
                  value={form.id}
                  onChange={(e) => F({ ...form, id: e.target.value })}
                />
              </label>
            )}
            <label>
              名称
              <Input
                required
                data-testid={form.kind + "-name"}
                value={form.name}
                onChange={(e) => F({ ...form, name: e.target.value })}
              />
            </label>
            {form.kind === "instance" ? (
              <>
                <label>
                  连接字符串
                  <Input.Password
                    required={!form.edit}
                    data-testid="instance-dsn"
                    placeholder={form.edit ? "留空保留现有连接" : ""}
                    value={form.dsn}
                    onChange={(e) => F({ ...form, dsn: e.target.value })}
                  />
                </label>
                <label>
                  备注
                  <Input
                    value={form.memo}
                    onChange={(e) => F({ ...form, memo: e.target.value })}
                  />
                </label>
              </>
            ) : (
              <label>
                允许修改数据{" "}
                <Switch
                  data-testid="token-allow-execute"
                  checked={form.allow_execute}
                  onChange={(v) => F({ ...form, allow_execute: v })}
                />
              </label>
            )}
            <Button
              type="primary"
              htmlType="submit"
              data-testid={form.kind + "-save"}
            >
              保存
            </Button>
          </form>
        )}
      </Modal>
      <Modal
        open={!!del}
        title="确认删除或撤销？"
        onCancel={() => X(null)}
        footer={
          <Button
            danger
            data-testid="confirm-delete"
            onClick={() =>
              run(async () => {
                await api(del.path, "DELETE");
                X(null);
                Q("");
                B(null);
                await load();
              })
            }
          >
            确认
          </Button>
        }
      >
        此操作将移除所选项目。
      </Modal>
    </main>
  );
}
createRoot(document.getElementById("root")).render(<App />);
