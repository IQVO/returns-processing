import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebar: SidebarsConfig = {
  apisidebar: [
    {
      type: "doc",
      id: "api-reference/rest/returns-processing-api",
    },
    {
      type: "category",
      label: "Returns",
      link: {
        type: "doc",
        id: "api-reference/rest/returns",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/authorize-return",
          label: "Authorize a return (open an RMA)",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/list-returns-by-order",
          label: "List the RMAs of an order",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-return",
          label: "Get an RMA",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Receipts",
      link: {
        type: "doc",
        id: "api-reference/rest/receipts",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/open-return-receipt",
          label: "Open a return receipt against an RMA",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/inspect-return-line",
          label: "Grade a receipt line and decide its disposition",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Dispositions",
      link: {
        type: "doc",
        id: "api-reference/rest/dispositions",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/override-disposition",
          label: "Override a disposition with a reason",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Disposition policies",
      link: {
        type: "doc",
        id: "api-reference/rest/disposition-policies",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/declare-disposition-policy",
          label: "Declare a disposition policy version",
          className: "api-method put",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-disposition-policy",
          label: "Get the current disposition policy",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Health",
      link: {
        type: "doc",
        id: "api-reference/rest/health",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/healthz",
          label: "Liveness probe",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/readyz",
          label: "Readiness probe",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/metrics",
          label: "Prometheus metrics",
          className: "api-method get",
        },
      ],
    },
  ],
};

export default sidebar.apisidebar;
