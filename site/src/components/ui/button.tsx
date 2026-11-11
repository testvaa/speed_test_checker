import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 rounded-[10px] border text-sm font-medium " +
    "px-5 py-[11px] no-underline cursor-pointer transition-colors duration-150 " +
    "active:translate-y-px disabled:opacity-45 disabled:pointer-events-none",
  {
    variants: {
      variant: {
        default: "border-[#2e2e34] bg-[#1c1c20] text-[#e6e6e9] hover:bg-[#26262c] hover:border-[#3c3c44]",
        primary:
          "border-[#265c46] bg-[#173227] text-[#7ce3b8] shadow-[0_0_24px_rgba(52,211,153,0.12)] " +
          "hover:bg-[#1b3f30] hover:border-[#2f7a5c]",
        danger: "border-[#6e2f2f] bg-[#3a1d1d] text-[#f8a3a3] hover:bg-[#472121] hover:border-[#8a3a3a]",
      },
    },
    defaultVariants: { variant: "default" },
  }
);

export type ButtonProps = ComponentProps<"a" | "button"> & VariantProps<typeof buttonVariants>;

export function Button({ className, variant, ...props }: ButtonProps) {
  return <a className={cn(buttonVariants({ variant }), className)} {...props} />;
}
