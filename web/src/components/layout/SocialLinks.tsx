"use client";

import clsx from "clsx";

type Variant = "header" | "overlay";

const SOCIAL_LINKS = [
  {
    id: "github",
    href: "https://github.com/wquguru/nof0",
    label: "Open GitHub repository",
    title: "GitHub",
    icon: (
      <svg
        className="w-full h-full"
        xmlns="http://www.w3.org/2000/svg"
        viewBox="0 0 24 24"
        fill="currentColor"
        aria-hidden="true"
      >
        <path d="M12 .5C5.73.5.97 5.26.97 11.54c0 4.86 3.15 8.98 7.52 10.43.55.1.75-.24.75-.53 0-.26-.01-1.13-.02-2.05-3.06.67-3.71-1.3-3.71-1.3-.5-1.28-1.22-1.63-1.22-1.63-.99-.68.08-.67.08-.67 1.09.08 1.66 1.12 1.66 1.12.98 1.67 2.56 1.19 3.19.91.1-.71.38-1.19.69-1.46-2.44-.28-5.01-1.22-5.01-5.42 0-1.2.43-2.18 1.12-2.95-.11-.28-.49-1.42.11-2.96 0 0 .93-.3 3.05 1.13.89-.25 1.84-.38 2.79-.38.95 0 1.9.13 2.79.38 2.12-1.43 3.05-1.13 3.05-1.13.6 1.54.22 2.68.11 2.96.69.77 1.12 1.75 1.12 2.95 0 4.21-2.57 5.14-5.02 5.41.39.34.73 1.01.73 2.03 0 1.46-.01 2.63-.01 2.98 0 .29.19.64.75.53 4.37-1.45 7.52-5.57 7.52-10.43C23.03 5.26 18.27.5 12 .5z" />
      </svg>
    ),
  },
  {
    id: "x",
    href: "https://twitter.com/intent/follow?screen_name=wquguru",
    label: "Follow on X (Twitter)",
    title: "Follow on X",
    icon: (
      <svg
        className="w-full h-full"
        xmlns="http://www.w3.org/2000/svg"
        viewBox="0 0 24 24"
        fill="currentColor"
        aria-hidden="true"
      >
        <path d="M18.244 2H21.5l-7.5 8.57L23 22h-6.555l-5.12-6.622L5.38 22H2.12l8.08-9.236L2 2h6.69l4.64 6.02L18.244 2zm-2.296 18h1.82L8.16 4H6.25l9.698 16z" />
      </svg>
    ),
  },
  {
    id: "telegram",
    href: "https://t.me/nof0_ai",
    label: "Join Telegram group",
    title: "Join Telegram",
    icon: (
      <svg
        className="w-full h-full"
        xmlns="http://www.w3.org/2000/svg"
        viewBox="0 0 24 24"
        fill="currentColor"
        aria-hidden="true"
      >
        <path d="M21.04 3.16 3.45 10.2c-1.21.48-1.2 1.16-.22 1.46l4.5 1.4 10.43-6.6c.5-.3.96-.14.58.18l-8.45 7.5-.32 4.66c.47 0 .68-.22.93-.47l2.24-2.17 4.67 3.37c.85.47 1.45.23 1.66-.78L22.7 4.7c.3-1.21-.46-1.76-1.66-1.54Z" />
      </svg>
    ),
  },
];

const variantStyles: Record<
  Variant,
  { buttonClass: string; iconClass: string; gapClass: string }
> = {
  header: {
    buttonClass: "w-7 h-7 text-[13px]",
    iconClass: "w-4 h-4",
    gapClass: "gap-2",
  },
  overlay: {
    buttonClass: "w-11 h-11 sm:w-12 sm:h-12 text-base",
    iconClass: "w-5 h-5",
    gapClass: "gap-3",
  },
};

type Props = {
  variant?: Variant;
  className?: string;
};

export function SocialLinks({ variant = "header", className }: Props) {
  const { buttonClass, iconClass, gapClass } = variantStyles[variant];

  return (
    <div className={clsx("flex items-center", gapClass, className)}>
      {SOCIAL_LINKS.map((link) => (
        <a
          key={link.id}
          href={link.href}
          target="_blank"
          rel="noopener noreferrer"
          aria-label={link.label}
          title={link.title}
          className={clsx(
            "inline-flex items-center justify-center rounded border chip-btn transition-colors",
            buttonClass,
          )}
          style={{
            borderColor: "var(--chip-border)",
            color: "var(--btn-inactive-fg)",
          }}
        >
          <span
            className={clsx(
              iconClass,
              "inline-flex items-center justify-center text-current",
            )}
            aria-hidden="true"
          >
            {link.icon}
          </span>
        </a>
      ))}
    </div>
  );
}

export default SocialLinks;
