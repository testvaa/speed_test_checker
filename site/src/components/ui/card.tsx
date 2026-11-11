import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

export function Card({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn("rounded-[14px] border border-line bg-panel p-[22px]", className)}
      {...props}
    />
  );
}
