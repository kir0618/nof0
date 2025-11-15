"use client";

import SocialLinks from "./SocialLinks";
import clsx from "clsx";

const RESOURCE_LINKS = [
  {
    id: "docs",
    label: "文档",
    href: "https://wquguru.gitbook.io/nof0",
    description: "产品背景与 Roadmap",
  },
  {
    id: "prompt",
    label: "逆向提示词",
    href: "https://gist.github.com/wquguru/7d268099b8c04b7e5b6ad6fae922ae83",
    description: "复盘当前策略提示词",
  },
];

export default function GlobalPauseOverlay() {
  return (
    <div
      className="fixed inset-x-0 bottom-0 top-[var(--header-h)] z-[70] pointer-events-auto"
      aria-live="polite"
    >
      <div
        className="absolute inset-0"
        style={{
          background: "var(--background)",
        }}
      />
      <div className="relative z-10 flex h-full w-full items-center justify-center px-4 py-8">
        <div
          className="w-full max-w-2xl rounded-3xl border px-6 py-7 sm:px-10 sm:py-9 text-center space-y-5 shadow-[0_20px_80px_rgba(0,0,0,0.45)] overflow-y-auto"
          style={{
            borderColor: "var(--panel-border)",
            background:
              "linear-gradient(135deg, rgba(255,255,255,0.08), rgba(255,255,255,0.02))",
            color: "var(--foreground)",
          }}
        >
          <div
            className="ui-sans text-[11px] tracking-[0.5em] uppercase"
            style={{ color: "var(--muted-text)" }}
          >
            公告
          </div>
          <div className="space-y-3">
            <p className="text-2xl sm:text-[28px] font-semibold leading-snug">
              第一季度已经结束，100%开源后端正在开发中
            </p>
            <p
              className="text-sm sm:text-base leading-relaxed"
              style={{ color: "var(--muted-text)" }}
            >
              /api/nof1 接口已暂停，现有页面内容暂为静态展示。我们正自建后端，
              完成后会第一时间恢复交互与数据推送。
            </p>
          </div>
          <div className="space-y-2">
            <div
              className="text-xs ui-sans tracking-[0.3em] uppercase"
              style={{ color: "var(--muted-text)" }}
            >
              主要渠道
            </div>
            <SocialLinks
              variant="overlay"
              className="justify-center flex-wrap"
            />
          </div>
          <div className="space-y-3">
            <div
              className="text-xs ui-sans tracking-[0.3em] uppercase"
              style={{ color: "var(--muted-text)" }}
            >
              更多资源
            </div>
            <div className="flex flex-col gap-3 sm:flex-row sm:gap-4 text-left">
              {RESOURCE_LINKS.map((item) => (
                <a
                  key={item.id}
                  href={item.href}
                  target="_blank"
                  rel="noopener noreferrer"
                  className={clsx(
                    "flex-1 rounded-2xl border px-4 py-3 transition-colors chip-btn",
                  )}
                  style={{ borderColor: "var(--panel-border)" }}
                >
                  <div className="ui-sans text-sm font-semibold">
                    {item.label}
                  </div>
                  <p
                    className="mt-1 text-xs leading-relaxed"
                    style={{ color: "var(--muted-text)" }}
                  >
                    {item.description}
                  </p>
                </a>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
