from django.shortcuts import render
from .models import Transaction

from rest_framework import (viewsets, permissions)
from .serializers import TransactionSerializer


class TransactionViewSet(viewsets.ModelViewSet):
    """
    API endpoint that allows showing and editing all transactions
    """

    queryset = Transaction.objects.all().order_by("-date_booking")
    serializer_class = TransactionSerializer
    permission_classes = [permissions.IsAuthenticated]


# def list_view(request):
#     context = {}
#     context["dataset"] = Transaction.objects().order_by("-date_booking")

#     return render(request, "main/index.html", context)


