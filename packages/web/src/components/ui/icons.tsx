import type { SVGProps } from 'react';

/**
 * 图标集。
 *
 * 手写内联 SVG 而不是引入图标库：面板一共需要二十来个图标，
 * 一个图标库会带进上千个用不到的路径（lucide-react 压缩后仍有 ~40KB），
 * 而这里全部加起来不到 4KB。
 *
 * 统一规格：24 网格、1.6 描边、round 端点 —— 与页面的圆角语言一致。
 * 尺寸由外部 className 控制，默认继承字号。
 */

type IconProps = SVGProps<SVGSVGElement>;

function Icon({ children, ...rest }: IconProps & { children: React.ReactNode }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.6}
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4"
      aria-hidden="true"
      {...rest}
    >
      {children}
    </svg>
  );
}

export const IconDashboard = (props: IconProps) => (
  <Icon {...props}>
    <rect x="3" y="3" width="7.5" height="9" rx="2" />
    <rect x="13.5" y="3" width="7.5" height="5.5" rx="2" />
    <rect x="13.5" y="11.5" width="7.5" height="9.5" rx="2" />
    <rect x="3" y="15" width="7.5" height="6" rx="2" />
  </Icon>
);

export const IconBot = (props: IconProps) => (
  <Icon {...props}>
    <rect x="4" y="7.5" width="16" height="11" rx="3" />
    <path d="M12 7.5V4M12 3.5a.5.5 0 1 0 0-.001" />
    <circle cx="9" cy="12.5" r="1.1" fill="currentColor" stroke="none" />
    <circle cx="15" cy="12.5" r="1.1" fill="currentColor" stroke="none" />
    <path d="M2 12v3M22 12v3" />
  </Icon>
);

export const IconSessions = (props: IconProps) => (
  <Icon {...props}>
    <path d="M20.5 12.5a7.5 7.5 0 0 1-10.9 6.7L4 20.5l1.4-5.3A7.5 7.5 0 1 1 20.5 12.5Z" />
    <path d="M9 11h6M9 14h4" />
  </Icon>
);

export const IconRules = (props: IconProps) => (
  <Icon {...props}>
    <path d="M12 3 4.5 6.5v5c0 4.6 3.1 8.4 7.5 9.5 4.4-1.1 7.5-4.9 7.5-9.5v-5L12 3Z" />
    <path d="m9 12 2 2 4-4" />
  </Icon>
);

export const IconAudit = (props: IconProps) => (
  <Icon {...props}>
    <path d="M5 4.5h9.5L19 9v11.5H5z" />
    <path d="M14 4.5V9h5" />
    <path d="M8.5 13h7M8.5 16.5h4.5" />
  </Icon>
);

export const IconSettings = (props: IconProps) => (
  <Icon {...props}>
    <circle cx="12" cy="12" r="3" />
    <path d="M12 2.5v2.2M12 19.3v2.2M21.5 12h-2.2M4.7 12H2.5M18.7 5.3l-1.6 1.6M6.9 17.1l-1.6 1.6M18.7 18.7l-1.6-1.6M6.9 6.9 5.3 5.3" />
  </Icon>
);

export const IconPlus = (props: IconProps) => (
  <Icon {...props}>
    <path d="M12 5v14M5 12h14" />
  </Icon>
);

export const IconSearch = (props: IconProps) => (
  <Icon {...props}>
    <circle cx="11" cy="11" r="6.5" />
    <path d="m16 16 4 4" />
  </Icon>
);

export const IconClose = (props: IconProps) => (
  <Icon {...props}>
    <path d="M6 6l12 12M18 6 6 18" />
  </Icon>
);

export const IconCheck = (props: IconProps) => (
  <Icon {...props}>
    <path d="m5 12.5 4.5 4.5L19 7" />
  </Icon>
);

export const IconCopy = (props: IconProps) => (
  <Icon {...props}>
    <rect x="9" y="9" width="11" height="11" rx="2.5" />
    <path d="M15 6.5V5.5A2.5 2.5 0 0 0 12.5 3h-7A2.5 2.5 0 0 0 3 5.5v7A2.5 2.5 0 0 0 5.5 15h1" />
  </Icon>
);

export const IconTrash = (props: IconProps) => (
  <Icon {...props}>
    <path d="M4 6.5h16M9.5 6.5V4.8A1.3 1.3 0 0 1 10.8 3.5h2.4a1.3 1.3 0 0 1 1.3 1.3v1.7" />
    <path d="M6.5 6.5 7.4 19a1.5 1.5 0 0 0 1.5 1.4h6.2a1.5 1.5 0 0 0 1.5-1.4l.9-12.5" />
    <path d="M10.5 10.5v6M13.5 10.5v6" />
  </Icon>
);

export const IconEdit = (props: IconProps) => (
  <Icon {...props}>
    <path d="M16.5 3.9a2.1 2.1 0 0 1 3 3L8.4 18l-4 1 1-4 11.1-11.1Z" />
  </Icon>
);

export const IconRefresh = (props: IconProps) => (
  <Icon {...props}>
    <path d="M20 11.5A8 8 0 1 0 18.4 17" />
    <path d="M20.5 6.5v5h-5" />
  </Icon>
);

export const IconChevronRight = (props: IconProps) => (
  <Icon {...props}>
    <path d="m9 5 7 7-7 7" />
  </Icon>
);

export const IconChevronDown = (props: IconProps) => (
  <Icon {...props}>
    <path d="m5 9 7 7 7-7" />
  </Icon>
);

export const IconLogout = (props: IconProps) => (
  <Icon {...props}>
    <path d="M15 8V5.5A2.5 2.5 0 0 0 12.5 3h-7A2.5 2.5 0 0 0 3 5.5v13A2.5 2.5 0 0 0 5.5 21h7a2.5 2.5 0 0 0 2.5-2.5V16" />
    <path d="M10 12h11m0 0-3.5-3.5M21 12l-3.5 3.5" />
  </Icon>
);

export const IconBan = (props: IconProps) => (
  <Icon {...props}>
    <circle cx="12" cy="12" r="8.5" />
    <path d="m6.5 6.5 11 11" />
  </Icon>
);

export const IconWarning = (props: IconProps) => (
  <Icon {...props}>
    <path d="M12 3.5 2.8 19.5h18.4L12 3.5Z" />
    <path d="M12 9.5v4.5M12 17h.01" />
  </Icon>
);

export const IconCommand = (props: IconProps) => (
  <Icon {...props}>
    <path d="M9 6.5a2.5 2.5 0 1 0-2.5 2.5H9v6.5a2.5 2.5 0 1 1-2.5-2.5H9m6 0h2.5A2.5 2.5 0 1 1 15 15.5V9m0 0h2.5A2.5 2.5 0 1 0 15 6.5V9m0 0H9" />
  </Icon>
);

export const IconSun = (props: IconProps) => (
  <Icon {...props}>
    <circle cx="12" cy="12" r="4" />
    <path d="M12 3v1.8M12 19.2V21M21 12h-1.8M4.8 12H3M18.4 5.6l-1.3 1.3M6.9 17.1l-1.3 1.3M18.4 18.4l-1.3-1.3M6.9 6.9 5.6 5.6" />
  </Icon>
);

export const IconMoon = (props: IconProps) => (
  <Icon {...props}>
    <path d="M20.5 14.3A8.5 8.5 0 0 1 9.7 3.5 8.5 8.5 0 1 0 20.5 14.3Z" />
  </Icon>
);

export const IconSend = (props: IconProps) => (
  <Icon {...props}>
    <path d="M20.5 3.5 10.8 13.2M20.5 3.5l-6 17-3.7-7.3-7.3-3.7 17-6Z" />
  </Icon>
);

export const IconInfo = (props: IconProps) => (
  <Icon {...props}>
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 11v5M12 8h.01" />
  </Icon>
);

export const IconExternal = (props: IconProps) => (
  <Icon {...props}>
    <path d="M14 4h6v6M20 4l-8.5 8.5" />
    <path d="M18 14.5v3A2.5 2.5 0 0 1 15.5 20h-9A2.5 2.5 0 0 1 4 17.5v-9A2.5 2.5 0 0 1 6.5 6h3" />
  </Icon>
);
